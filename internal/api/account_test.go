// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"
)

func TestOwnAccount(t *testing.T) {
	ts := newTestServer(t)
	phone := ts.signIn(t, "ada", true, "phone")
	var other loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("ada", "correct horse", "laptop"), &other)

	var me struct {
		User struct {
			Name        string `json:"name"`
			IsAdmin     bool   `json:"is_admin"`
			HasPassword bool   `json:"has_password"`
		} `json:"user"`
		Device myDeviceBody `json:"device"`
	}
	if rec := ts.call(t, "GET", "/api/v1/auth/me", phone, "", nil, &me); rec.Code != 200 || me.User.Name != "ada" || !me.User.IsAdmin ||
		!me.User.HasPassword || me.Device.Name != "Test device" || !me.Device.Current {
		t.Fatalf("me: %d %+v", rec.Code, me)
	}
	var devices struct {
		Devices []myDeviceBody `json:"devices"`
	}
	ts.call(t, "GET", "/api/v1/auth/me/devices", phone, "", nil, &devices)
	if len(devices.Devices) != 2 {
		t.Fatalf("devices %+v", devices)
	}
	var laptop myDeviceBody
	for _, d := range devices.Devices {
		if !d.Current {
			laptop = d
		}
	}

	// The password: a wrong current one counts with failed sign-ins, a
	// short new one is refused, and the tokens stay valid.
	if rec := ts.call(t, "PUT", "/api/v1/auth/me/password", phone, "", map[string]string{"current": "wrong horse", "new": "a new long password"}, nil); rec.Code != 401 {
		t.Fatalf("a wrong password: %d", rec.Code)
	}
	if rec := ts.call(t, "PUT", "/api/v1/auth/me/password", phone, "", map[string]string{"current": "correct horse", "new": "short"}, nil); rec.Code != 400 {
		t.Fatalf("a short password: %d", rec.Code)
	}
	if rec := ts.call(t, "PUT", "/api/v1/auth/me/password", phone, "", map[string]string{"current": "correct horse", "new": "a new long password"}, nil); rec.Code != 204 {
		t.Fatalf("a new password: %d", rec.Code)
	}
	if rec := ts.call(t, "POST", "/api/v1/auth/login", "", "", login("ada", "a new long password", "tablet"), nil); rec.Code != 200 {
		t.Fatalf("sign in with it: %d", rec.Code)
	}
	if rec := ts.call(t, "GET", "/api/v1/auth/me", other.Token, "", nil, nil); rec.Code != 200 {
		t.Fatalf("the laptop after the change: %d", rec.Code)
	}

	// Sign the laptop out from the phone; someone else's device is not one
	// of mine.
	if rec := ts.call(t, "DELETE", "/api/v1/auth/me/devices/"+idStr(laptop.ID), phone, "", nil, nil); rec.Code != 204 {
		t.Fatalf("sign out the laptop: %d", rec.Code)
	}
	if rec := ts.call(t, "GET", "/api/v1/auth/me", other.Token, "", nil, nil); rec.Code != 401 {
		t.Fatalf("the laptop after its sign-out: %d", rec.Code)
	}
	noor := ts.signIn(t, "noor", false, "noor-phone")
	var theirs struct {
		Devices []myDeviceBody `json:"devices"`
	}
	ts.call(t, "GET", "/api/v1/auth/me/devices", noor, "", nil, &theirs)
	if rec := ts.call(t, "DELETE", "/api/v1/auth/me/devices/"+idStr(theirs.Devices[0].ID), phone, "", nil, nil); rec.Code != 404 {
		t.Fatalf("another person's device: %d", rec.Code)
	}

	// Someone who signs in by pairing only has no password to change.
	kimUser := ts.addUser(t, "kim", "", false)
	p, _ := ts.startPairing(t, "192.0.2.5:1", "kim-tv")
	ts.call(t, "POST", "/api/v1/auth/pair/approve", phone, "", map[string]any{"code": p.Code, "user_id": kimUser.ID}, nil)
	var kim loginAnswer
	ts.call(t, "POST", "/api/v1/auth/pair/poll", "", "", map[string]string{"code": p.Code, "secret": p.Secret}, &kim)
	if rec := ts.call(t, "PUT", "/api/v1/auth/me/password", kim.Token, "", map[string]string{"current": "", "new": "a new long password"}, nil); rec.Code != 403 {
		t.Fatalf("a pairing-only password: %d", rec.Code)
	}

	var users struct {
		Users []struct {
			Name         string `json:"name"`
			HasPassword  bool   `json:"has_password"`
			Devices      int    `json:"devices"`
			LastActiveAt *int64 `json:"last_active_at"`
		} `json:"users"`
	}
	if rec := ts.call(t, "GET", "/api/v1/admin/users", phone, "", nil, &users); rec.Code != 200 || len(users.Users) != 3 ||
		users.Users[0].Name != "ada" || users.Users[0].Devices != 2 || users.Users[1].Name != "kim" || users.Users[1].HasPassword ||
		users.Users[1].LastActiveAt == nil {
		t.Fatalf("users %d %+v", rec.Code, users)
	}

	// Signing out.
	if rec := ts.call(t, "POST", "/api/v1/auth/logout", phone, "", nil, nil); rec.Code != 204 {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := ts.call(t, "GET", "/api/v1/auth/me", phone, "", nil, nil); rec.Code != 401 {
		t.Fatalf("after logout: %d", rec.Code)
	}
}
