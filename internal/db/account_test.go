// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"errors"
	"testing"

	"github.com/gureetk/runnel/internal/model"
)

// peopleFixture makes ada, the first and an admin, noor and bo, each
// signed in on a device of their own, and a playback of ada's in the
// start-time log.
func peopleFixture(t *testing.T) (*DB, []model.User) {
	t.Helper()
	d, _ := openTemp(t)
	ctx := context.Background()
	var users []model.User
	for _, name := range []string{"ada", "noor", "bo"} {
		u, err := d.CreateUser(ctx, name, &model.Password{Hash: []byte("h"), Salt: []byte("s"), Iter: 1}, name == "bo", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.ReplaceToken(ctx, u.ID, model.Device{ID: "d-" + name, Name: name + "'s phone", Client: "runnel-test/0"},
			[]byte("token of "+name), 2); err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}
	if err := d.LogPlay(ctx, PlayStart{At: 3, UserID: users[0].ID, Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	return d, users
}

func TestChangeUser(t *testing.T) {
	d, u := peopleFixture(t)
	ctx := context.Background()
	ada, noor, bo := u[0], u[1], u[2]
	str := func(s string) *string { return &s }
	no, yes := false, true

	for _, c := range []struct {
		id     int64
		change UserChange
		want   error
	}{
		{noor.ID, UserChange{Name: str("ADA")}, ErrNameTaken},
		{99, UserChange{Name: str("x")}, ErrNotFound},
		{bo.ID, UserChange{IsAdmin: &no}, nil},
		{ada.ID, UserChange{IsAdmin: &no, Name: str("ada2")}, ErrLastAdmin},
		{noor.ID, UserChange{Name: str("Noor"), NoPassword: true, IsAdmin: &yes}, nil},
		{ada.ID, UserChange{IsAdmin: &no, Password: &model.Password{Hash: []byte("new"), Salt: []byte("s"), Iter: 2}}, nil},
	} {
		if err := d.ChangeUser(ctx, c.id, c.change); !errors.Is(err, c.want) {
			t.Errorf("%d %+v: %v, want %v", c.id, c.change, err, c.want)
		}
	}
	users, _ := d.Users(ctx)
	byID := map[int64]UserRow{}
	for _, r := range users {
		byID[r.ID] = r
	}
	if r := byID[ada.ID]; r.Name != "ada" || r.IsAdmin || !r.HasPassword || r.Devices != 1 {
		t.Fatalf("ada: %+v", r)
	}
	if pw, _ := d.PasswordOf(ctx, ada.ID); pw == nil || string(pw.Hash) != "new" {
		t.Fatalf("ada's password: %+v", pw)
	}
	if r := byID[noor.ID]; r.Name != "Noor" || !r.IsAdmin || r.HasPassword || r.Devices != 1 {
		t.Fatalf("noor: %+v", r)
	}
	if r := byID[bo.ID]; r.IsAdmin {
		t.Fatalf("bo: %+v", r)
	}
}

func TestDeleteUser(t *testing.T) {
	d, u := peopleFixture(t)
	ctx := context.Background()
	ada, noor, bo := u[0], u[1], u[2]
	no := false
	d.ChangeUser(ctx, bo.ID, UserChange{IsAdmin: &no})
	if err := d.DeleteUser(ctx, ada.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("the last admin: %v", err)
	}
	if err := d.DeleteUser(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such user: %v", err)
	}
	if err := d.DeleteUser(ctx, noor.ID); err != nil {
		t.Fatal(err)
	}
	yes := true
	d.ChangeUser(ctx, bo.ID, UserChange{IsAdmin: &yes})
	if err := d.DeleteUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM tokens`); n != 1 {
		t.Fatalf("%d tokens left", n)
	}
	// The log keeps the row, with the name it had, and without the id.
	rows, err := d.PlayLog(ctx, 0, 10)
	if err != nil || len(rows) != 1 || rows[0].UserID != 0 || rows[0].User != "ada" {
		t.Fatalf("%+v %v", rows, err)
	}
	// Neither a removed user's id nor a removed token's is given again.
	cy, err := d.CreateUser(ctx, "cy", nil, false, 5)
	if err != nil || cy.ID <= bo.ID {
		t.Fatalf("a new user took id %d after %d: %v", cy.ID, bo.ID, err)
	}
	var highest int64
	d.r.QueryRow(`SELECT max(id) FROM tokens`).Scan(&highest)
	if err := d.RevokeToken(ctx, highest); err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceToken(ctx, cy.ID, model.Device{ID: "d-cy"}, []byte("token of cy"), 6); err != nil {
		t.Fatal(err)
	}
	devices, _ := d.AllDevices(ctx)
	if len(devices) != 1 || devices[0].ID <= highest || devices[0].UserName != "cy" {
		t.Fatalf("devices %+v after token %d went", devices, highest)
	}
	if err := d.RevokeToken(ctx, highest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a token that went: %v", err)
	}
}

func TestAllDevices(t *testing.T) {
	d, u := peopleFixture(t)
	ctx := context.Background()
	if err := d.TouchToken(ctx, 2, 50); err != nil {
		t.Fatal(err)
	}
	devices, err := d.AllDevices(ctx)
	if err != nil || len(devices) != 3 {
		t.Fatalf("%+v %v", devices, err)
	}
	if first := devices[0]; first.ID != 2 || first.UserID != u[1].ID || first.UserName != "noor" || first.Name != "noor's phone" ||
		first.Client != "runnel-test/0" || first.LastUsedAt != 50 || first.SignedInAt != 2 {
		t.Fatalf("the one used last: %+v", first)
	}
}
