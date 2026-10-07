package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestUserUpdateFields pins the partial user write on both drivers: it sets
// the columns it names and no other, a column it does not name keeps what an
// earlier write set, naming none writes nothing and answers the row, and a
// deleted or unknown id is ErrNotFound with a deleted row's columns kept.
func TestUserUpdateFields(t *testing.T) {
	str := func(s string) *string { return &s }
	yes := true
	cases := []struct {
		name    string
		deleted bool
		unknown bool
		f       UserFields
		want    func(u User) User // the row after the call, from the row before
		wantErr error
	}{
		{name: "one field", f: UserFields{Email: str("ada@y.io")},
			want: func(u User) User { u.Email = "ada@y.io"; return u }},
		{name: "all ten fields", f: UserFields{Email: str("ada@z.io"), Display: str("Ada L"), Title: str(""), Status: str(UserDisabled),
			PasswordHash: str("new-hash"), UserType: str(UserTypeAgent), AgencyMode: str(AgencyAutonomous),
			Sponsor: str("kim"), SwarmID: str("s-2"), Ephemeral: &yes},
			want: func(u User) User {
				u.Email, u.Display, u.Title, u.Status, u.PasswordHash = "ada@z.io", "Ada L", "", UserDisabled, "new-hash"
				u.UserType, u.AgencyMode, u.Sponsor, u.SwarmID, u.Ephemeral = UserTypeAgent, AgencyAutonomous, "kim", "s-2", true
				return u
			}},
		{name: "none", want: func(u User) User { return u }},
		{name: "a deleted row", deleted: true, f: UserFields{Email: str("ada@y.io"), Title: str("Lead")}, wantErr: ErrNotFound},
		{name: "a deleted row with no field", deleted: true, wantErr: ErrNotFound},
		{name: "an unknown id", unknown: true, f: UserFields{Email: str("ada@y.io")}, wantErr: ErrNotFound},
	}
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		for i, tc := range cases {
			u, err := s.Users().Create(ctx, User{Username: fmt.Sprintf("ada%d", i), Email: "ada@x.io", Display: "Ada",
				Title: "Engineer", PasswordHash: "old-hash", UserType: UserTypeHuman, AgencyMode: AgencyInteractive,
				Sponsor: "lin", SwarmID: "s-1"})
			if err != nil {
				t.Fatal(err)
			}
			id := u.ID
			if tc.deleted {
				if _, err := s.Users().SoftDelete(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unknown {
				id = "no-such-user"
			}
			before, _ := s.Users().GetByID(ctx, id)
			got, err := s.Users().UpdateFields(ctx, id, tc.f)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("%s: UpdateFields = %v, want %v", tc.name, err, tc.wantErr)
				}
				// The read-back answers ErrNotFound for a deleted row whether
				// or not the write reached it, so the columns are read raw.
				var email, title string
				if err := s.(*sqlStore).queryRow(ctx, `SELECT email, title FROM users WHERE id = $1`, u.ID).Scan(&email, &title); err != nil {
					t.Fatal(err)
				}
				if email != "ada@x.io" || title != "Engineer" {
					t.Errorf("%s: the row holds email %q and title %q after the call, want them untouched", tc.name, email, title)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s: UpdateFields: %v", tc.name, err)
			}
			read, err := s.Users().GetByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if got != read {
				t.Errorf("%s: the answer %+v is not the stored row %+v", tc.name, got, read)
			}
			want := tc.want(before)
			if tc.f == (UserFields{}) {
				if !got.UpdatedAt.Equal(before.UpdatedAt) {
					t.Errorf("%s: updated_at moved from %v to %v on a write of no field", tc.name, before.UpdatedAt, got.UpdatedAt)
				}
			} else if got.UpdatedAt.Before(before.UpdatedAt) {
				t.Errorf("%s: updated_at went back from %v to %v", tc.name, before.UpdatedAt, got.UpdatedAt)
			}
			want.UpdatedAt, want.CreatedAt = got.UpdatedAt, got.CreatedAt
			if got.CreatedAt != before.CreatedAt {
				t.Errorf("%s: created_at moved", tc.name)
			}
			if got != want {
				t.Errorf("%s: row after the call\n got %+v\nwant %+v", tc.name, got, want)
			}
		}

		// Two writes from the same stale read that name different columns
		// keep both values.
		u, err := s.Users().Create(ctx, User{Username: "ada-pair", Email: "ada@x.io", Title: "Engineer"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Users().UpdateFields(ctx, u.ID, UserFields{Email: str("ada@y.io")}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Users().UpdateFields(ctx, u.ID, UserFields{Title: str("Lead")}); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Users().GetByID(ctx, u.ID); err != nil || got.Email != "ada@y.io" || got.Title != "Lead" {
			t.Errorf("after an email write and a title write the row holds %q and %q, %v; want both values", got.Email, got.Title, err)
		}
	})
}

// TestChangedUserFields pins the partial write a PATCH derives from the row
// it read and the row it wants: one pointer per column that differs, each
// to the wanted value, and none for a column that holds the wanted value.
func TestChangedUserFields(t *testing.T) {
	read := User{ID: "u1", Username: "ada", Email: "ada@x.io", Display: "Ada", Title: "Engineer", Status: UserActive,
		PasswordHash: "old-hash", UserType: UserTypeHuman, AgencyMode: AgencyInteractive, Sponsor: "lin", SwarmID: "s-1"}
	cases := []struct {
		name string
		edit func(u *User)
		want func(f UserFields) bool
	}{
		{"nothing differs", func(*User) {}, func(f UserFields) bool { return f == UserFields{} }},
		{"email", func(u *User) { u.Email = "ada@y.io" },
			func(f UserFields) bool {
				return f.Email != nil && *f.Email == "ada@y.io" && f == UserFields{Email: f.Email}
			}},
		{"status", func(u *User) { u.Status = UserDisabled },
			func(f UserFields) bool {
				return f.Status != nil && *f.Status == UserDisabled && f == UserFields{Status: f.Status}
			}},
		{"password hash", func(u *User) { u.PasswordHash = "new-hash" },
			func(f UserFields) bool {
				return f.PasswordHash != nil && *f.PasswordHash == "new-hash" && f == UserFields{PasswordHash: f.PasswordHash}
			}},
		{"ephemeral", func(u *User) { u.Ephemeral = true },
			func(f UserFields) bool {
				return f.Ephemeral != nil && *f.Ephemeral && f == UserFields{Ephemeral: f.Ephemeral}
			}},
		{"sponsor cleared and swarm moved", func(u *User) { u.Sponsor, u.SwarmID = "", "s-2" },
			func(f UserFields) bool {
				return f.Sponsor != nil && *f.Sponsor == "" && f.SwarmID != nil && *f.SwarmID == "s-2" &&
					f == UserFields{Sponsor: f.Sponsor, SwarmID: f.SwarmID}
			}},
		{"all ten", func(u *User) {
			u.Email, u.Display, u.Title, u.Status, u.PasswordHash = "e", "d", "t", UserDisabled, "h"
			u.UserType, u.AgencyMode, u.Sponsor, u.SwarmID, u.Ephemeral = UserTypeAgent, AgencyAutonomous, "kim", "s-2", true
		}, func(f UserFields) bool {
			for _, p := range []*string{f.Email, f.Display, f.Title, f.Status, f.PasswordHash, f.UserType, f.AgencyMode, f.Sponsor, f.SwarmID} {
				if p == nil {
					return false
				}
			}
			return *f.Email == "e" && *f.Display == "d" && *f.Title == "t" && *f.Status == UserDisabled && *f.PasswordHash == "h" &&
				*f.UserType == UserTypeAgent && *f.AgencyMode == AgencyAutonomous && *f.Sponsor == "kim" && *f.SwarmID == "s-2" &&
				f.Ephemeral != nil && *f.Ephemeral
		}},
	}
	for _, tc := range cases {
		want := read
		tc.edit(&want)
		if f := ChangedUserFields(read, want); !tc.want(f) {
			t.Errorf("%s: ChangedUserFields = %+v", tc.name, f)
		}
	}
}
