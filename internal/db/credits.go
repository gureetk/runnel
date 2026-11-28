// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// CreditIn is one credit of an item as a provider gives it: the person,
// and what they did (docs/03-data-model.md, section 4, People and
// credits).
type CreditIn struct {
	ProviderID string // the person's id at the provider
	Name       string
	KnownFor   string // the department the person is known for, or ""
	PhotoRef   string // the provider's path of their photo, or ""
	PhotoKey   string // the image key derived from PhotoRef, or ""
	Kind       string // "cast" or "crew"
	Role       string // the character, or the job
	Department string // crew: "Directing", "Writing"
	Episodes   int    // series: how many episodes; 0 when not known
}

// Credit is one credit of an item as it is shown.
type Credit struct {
	PersonID   int64
	Name       string
	Kind       string
	Role       string
	Department string
	Episodes   int // 0 when not known
	PhotoKey   string
	PhotoColor string
}

// Person is one person, as GET /people/{id} shows them.
type Person struct {
	ID         int64
	Name       string
	Department string
	PhotoKey   string
	PhotoColor string
	Titles     int // the films and series that credit them
}

// ApplyCredits replaces an item's credits with those given, in the order
// shown, and the people they name, from provider. The item is stamped
// when what its credits show changed. A person's photo key changes only
// with its path; a new one is fetched again when first asked for.
func (d *DB) ApplyCredits(ctx context.Context, itemID int64, provider string, credits []CreditIn) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		old, err := creditsOf(ctx, tx.tx, itemID)
		if err != nil {
			return err
		}
		ids := map[string]int64{}
		for _, c := range credits {
			if _, ok := ids[c.ProviderID]; ok {
				continue
			}
			id, err := tx.person(ctx, provider, c)
			if err != nil {
				return err
			}
			ids[c.ProviderID] = id
		}
		next := make([]Credit, len(credits))
		for i, c := range credits {
			next[i] = Credit{PersonID: ids[c.ProviderID], Name: c.Name, Kind: c.Kind, Role: c.Role,
				Department: c.Department, Episodes: c.Episodes, PhotoKey: c.PhotoKey}
		}
		same := len(old) == len(next)
		for i := 0; same && i < len(old); i++ {
			o := old[i]
			o.PhotoColor = ""
			same = o == next[i]
		}
		if same {
			return nil
		}
		if _, err := tx.tx.ExecContext(ctx, `DELETE FROM credits WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		for i, c := range credits {
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO credits (item_id, ord, person_id, kind, role, department, episodes)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, itemID, i, ids[c.ProviderID], c.Kind, nullString(c.Role),
				nullString(c.Department), nullInt(int64(c.Episodes))); err != nil {
				return err
			}
		}
		return tx.stampItems(ctx, []int64{itemID})
	})
	if err != nil {
		return fmt.Errorf("db: the credits of item %d: %w", itemID, err)
	}
	return nil
}

// person writes one person as a credit names them, and returns their id.
// A person whose row already says the same is not written.
func (t *Tx) person(ctx context.Context, provider string, c CreditIn) (int64, error) {
	var id int64
	var name string
	var dept, ref sql.NullString
	err := t.tx.QueryRowContext(ctx, `SELECT id, name, department, image_ref FROM people WHERE provider = ? AND provider_id = ?`,
		provider, c.ProviderID).Scan(&id, &name, &dept, &ref)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if found && ref.String == c.PhotoRef {
		if name != c.Name || dept.String != c.KnownFor {
			_, err = t.tx.ExecContext(ctx, `UPDATE people SET name = ?, department = ? WHERE id = ?`, c.Name, nullString(c.KnownFor), id)
		}
		return id, err
	}
	// A key is one person's: another person's row that has it gives it
	// up, as when the provider moved a photo from one person to another.
	if c.PhotoKey != "" {
		if _, err := t.tx.ExecContext(ctx, `UPDATE people SET image_key = NULL, image_ref = NULL, image_color = NULL,
			image_fetched_at = NULL, image_tried_at = NULL
			WHERE image_key = ? AND NOT (provider = ? AND provider_id = ?)`, c.PhotoKey, provider, c.ProviderID); err != nil {
			return 0, err
		}
	}
	if !found {
		if id, err = t.newID(ctx, "last_person_id", "people"); err != nil {
			return 0, err
		}
		_, err = t.tx.ExecContext(ctx, `INSERT INTO people (id, provider, provider_id, name, department, image_key, image_ref)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, provider, c.ProviderID, c.Name, nullString(c.KnownFor),
			nullString(c.PhotoKey), nullString(c.PhotoRef))
		return id, err
	}
	_, err = t.tx.ExecContext(ctx, `UPDATE people SET name = ?, department = ?, image_key = ?, image_ref = ?,
		image_color = NULL, image_fetched_at = NULL, image_tried_at = NULL WHERE id = ?`,
		c.Name, nullString(c.KnownFor), nullString(c.PhotoKey), nullString(c.PhotoRef), id)
	return id, err
}

// Credits reads an item's credits, in the order shown.
func (d *DB) Credits(ctx context.Context, itemID int64) ([]Credit, error) {
	out, err := creditsOf(ctx, d.r, itemID)
	if err != nil {
		return nil, fmt.Errorf("db: read the credits of item %d: %w", itemID, err)
	}
	return out, nil
}

func creditsOf(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, itemID int64) ([]Credit, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.name, c.kind, COALESCE(c.role, ''), COALESCE(c.department, ''),
		COALESCE(c.episodes, 0), COALESCE(p.image_key, ''), COALESCE(p.image_color, '')
		FROM credits c JOIN people p ON p.id = c.person_id WHERE c.item_id = ? ORDER BY c.ord`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credit
	for rows.Next() {
		var c Credit
		if err := rows.Scan(&c.PersonID, &c.Name, &c.Kind, &c.Role, &c.Department, &c.Episodes, &c.PhotoKey, &c.PhotoColor); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PersonOf reads one person, with how many films and series credit them.
// ErrNotFound: no such person.
func (d *DB) PersonOf(ctx context.Context, id int64) (Person, error) {
	var p Person
	err := d.r.QueryRowContext(ctx, `SELECT p.id, p.name, COALESCE(p.department, ''), COALESCE(p.image_key, ''),
		COALESCE(p.image_color, ''), (SELECT count(DISTINCT c.item_id) FROM credits c WHERE c.person_id = p.id)
		FROM people p WHERE p.id = ?`, id).Scan(&p.ID, &p.Name, &p.Department, &p.PhotoKey, &p.PhotoColor, &p.Titles)
	if errors.Is(err, sql.ErrNoRows) {
		return Person{}, ErrNotFound
	}
	if err != nil {
		return Person{}, fmt.Errorf("db: read person %d: %w", id, err)
	}
	return p, nil
}

// PersonCredit is how one title credits a person, as a list of the
// person's titles shows it: the credit of theirs shown first.
type PersonCredit struct {
	Kind     string
	Role     string
	Episodes int
}

// CreditsOfPerson reads, for each of the items given, the credit of the
// person's that the item shows first.
func (d *DB) CreditsOfPerson(ctx context.Context, personID int64, itemIDs []int64) (map[int64]PersonCredit, error) {
	out := map[int64]PersonCredit{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	args := []any{personID}
	for _, id := range itemIDs {
		args = append(args, id)
	}
	rows, err := d.r.QueryContext(ctx, `SELECT c.item_id, c.kind, COALESCE(c.role, ''), COALESCE(c.episodes, 0), min(c.ord)
		FROM credits c WHERE c.person_id = ? AND c.item_id IN (?`+strings.Repeat(", ?", len(itemIDs)-1)+`)
		GROUP BY c.item_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("db: read the credits of person %d: %w", personID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c PersonCredit
		var ord int
		if err := rows.Scan(&id, &c.Kind, &c.Role, &c.Episodes, &ord); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

// DropUncredited deletes the people no item credits any longer
// (docs/03-data-model.md, section 4), and returns how many went.
func (d *DB) DropUncredited(ctx context.Context) (int, error) {
	var n int64
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		res, err := tx.tx.ExecContext(ctx, `DELETE FROM people WHERE NOT EXISTS (SELECT 1 FROM credits c WHERE c.person_id = people.id)`)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("db: drop people without credits: %w", err)
	}
	return int(n), nil
}
