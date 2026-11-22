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

func TestPeopleAndDevices(t *testing.T) {
	ts := newTestServer(t)
	ada := ts.signIn(t, "ada", true, "phone")
	noor := ts.signIn(t, "noor", false, "tv")
	var me loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("noor", "correct horse", "laptop"), &me)
	users := map[string]adminUserBody{}
	list := func() {
		t.Helper()
		var body struct {
			Users []adminUserBody `json:"users"`
		}
		ts.call(t, "GET", "/api/v1/admin/users", ada, "", nil, &body)
		users = map[string]adminUserBody{}
		for _, u := range body.Users {
			users[u.Name] = u
		}
	}
	list()
	adaID, noorID := idStr(users["ada"].ID), idStr(users["noor"].ID)

	var e apiError
	for _, c := range []struct {
		id     string
		body   any
		status int
		code   string
	}{
		{noorID, map[string]any{"name": "ADA"}, 409, "conflict"},
		{noorID, map[string]any{"name": ""}, 400, "bad_request"},
		{noorID, map[string]any{"name": nil}, 400, "bad_request"},
		{noorID, map[string]any{"password": "short"}, 400, "weak_password"},
		{noorID, map[string]any{"password": 7}, 400, "bad_request"},
		{noorID, map[string]any{"is_admin": "yes"}, 400, "bad_request"},
		{noorID, map[string]any{"devices": 0}, 400, "bad_request"},
		{adaID, map[string]any{"is_admin": false}, 409, "last_admin"},
		{"99", map[string]any{"name": "x"}, 404, "not_found"},
	} {
		e = apiError{}
		if rec := ts.call(t, "PATCH", "/api/v1/admin/users/"+c.id, ada, "", c.body, &e); rec.Code != c.status || e.Error.Code != c.code {
			t.Errorf("%s %v: %d %+v, want %d %s", c.id, c.body, rec.Code, e, c.status, c.code)
		}
	}
	var changed adminUserBody
	if rec := ts.call(t, "PATCH", "/api/v1/admin/users/"+noorID, ada, "",
		map[string]any{"name": "Noor", "password": nil, "is_admin": true}, &changed); rec.Code != 200 ||
		changed.Name != "Noor" || !changed.IsAdmin || changed.HasPassword || changed.Devices != 2 || changed.LastActiveAt == nil {
		t.Fatalf("%d %+v", rec.Code, changed)
	}
	if rec := ts.call(t, "POST", "/api/v1/auth/login", "", "", login("noor", "correct horse", "tablet"), nil); rec.Code != 401 {
		t.Fatalf("a password taken away still signs in: %d", rec.Code)
	}
	if rec := ts.call(t, "PATCH", "/api/v1/admin/users/"+noorID, ada, "", map[string]any{"password": "a new long password"}, &changed); rec.Code != 200 || !changed.HasPassword {
		t.Fatalf("a new password: %d %+v", rec.Code, changed)
	}
	if rec := ts.call(t, "POST", "/api/v1/auth/login", "", "", login("noor", "a new long password", "tablet"), nil); rec.Code != 200 {
		t.Fatalf("sign in with the new password: %d", rec.Code)
	}
	// Noor's devices stayed signed in, and noor is an admin now.
	if rec := ts.call(t, "GET", "/api/v1/admin/users", noor, "", nil, nil); rec.Code != 200 {
		t.Fatalf("noor's tv after the changes: %d", rec.Code)
	}

	var devices struct {
		Devices []struct {
			myDeviceBody
			User struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"user"`
		} `json:"devices"`
	}
	ts.call(t, "GET", "/api/v1/admin/tokens", ada, "", nil, &devices)
	if len(devices.Devices) != 4 {
		t.Fatalf("%+v", devices)
	}
	var tv struct {
		Device myDeviceBody `json:"device"`
	}
	ts.call(t, "GET", "/api/v1/auth/me", noor, "", nil, &tv)
	var laptop int64
	current := 0
	for _, d := range devices.Devices {
		if d.Current {
			current++
			if d.User.Name != "ada" {
				t.Fatalf("the current device is %+v", d)
			}
		}
		if d.User.Name == "Noor" && d.ID != tv.Device.ID {
			laptop = d.ID
		}
	}
	if current != 1 || laptop == 0 || tv.Device.ID == 0 {
		t.Fatalf("%d current, another device %d, the tv %d: %+v", current, laptop, tv.Device.ID, devices)
	}
	if rec := ts.call(t, "DELETE", "/api/v1/admin/tokens/"+idStr(laptop), ada, "", nil, nil); rec.Code != 204 {
		t.Fatalf("sign a device out: %d", rec.Code)
	}
	if rec := ts.call(t, "DELETE", "/api/v1/admin/tokens/"+idStr(laptop), ada, "", nil, &e); rec.Code != 404 || e.Error.Code != "not_found" {
		t.Fatalf("again: %d %+v", rec.Code, e)
	}

	// Noor removes ada, who is no longer the last admin. Ada's phone is
	// signed out with her.
	if rec := ts.call(t, "DELETE", "/api/v1/admin/users/"+adaID, noor, "", nil, nil); rec.Code != 204 {
		t.Fatalf("remove ada: %d", rec.Code)
	}
	if rec := ts.call(t, "GET", "/api/v1/auth/me", ada, "", nil, nil); rec.Code != 401 {
		t.Fatalf("ada's phone: %d", rec.Code)
	}
	if rec := ts.call(t, "DELETE", "/api/v1/admin/users/"+noorID, noor, "", nil, &e); rec.Code != 409 || e.Error.Code != "last_admin" {
		t.Fatalf("the last admin: %d %+v", rec.Code, e)
	}
	if rec := ts.call(t, "DELETE", "/api/v1/admin/users/"+adaID, noor, "", nil, &e); rec.Code != 404 {
		t.Fatalf("a user who went: %d", rec.Code)
	}
	// Someone who is no admin is refused all of it.
	bo := ts.signIn(t, "bo", false, "watch")
	for _, c := range [][2]string{{"PATCH", "/api/v1/admin/users/" + noorID}, {"DELETE", "/api/v1/admin/users/" + noorID},
		{"GET", "/api/v1/admin/tokens"}, {"DELETE", "/api/v1/admin/tokens/1"}} {
		if rec := ts.call(t, c[0], c[1], bo, "", map[string]any{}, nil); rec.Code != 403 {
			t.Errorf("%s %s by bo: %d", c[0], c[1], rec.Code)
		}
	}
}
