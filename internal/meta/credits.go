// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta/tmdb"
)

// MaxCast is how many of the cast an item keeps, in billing order (P7).
const MaxCast = 20

// filmCredits are the credits a film keeps (docs/06-library-and-metadata.md,
// section 6.3): the first MaxCast of the cast in billing order, then the
// directors, then the writers, each once, as TMDB lists them.
func filmCredits(c tmdb.Credits) []db.CreditIn {
	cast := slices.Clone(c.Cast)
	slices.SortStableFunc(cast, func(a, b tmdb.CastCredit) int { return cmp.Compare(a.Order, b.Order) })
	var out []db.CreditIn
	seen := map[int]bool{}
	for _, p := range cast {
		if len(out) == MaxCast {
			break
		}
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		out = append(out, credit(p.Person, "cast", strings.TrimSpace(p.Character), "", 0))
	}
	for _, dept := range []string{"Directing", "Writing"} {
		jobs := map[int][]string{}
		var order []tmdb.Person
		for _, p := range c.Crew {
			if dept == "Directing" && p.Job != "Director" || dept == "Writing" && p.Department != "Writing" {
				continue
			}
			if _, ok := jobs[p.ID]; !ok {
				order = append(order, p.Person)
			}
			if job := strings.TrimSpace(p.Job); job != "" && !slices.Contains(jobs[p.ID], job) {
				jobs[p.ID] = append(jobs[p.ID], job)
			}
		}
		for _, p := range order {
			out = append(out, credit(p, "crew", strings.Join(jobs[p.ID], ", "), dept, 0))
		}
	}
	return out
}

// seriesCredits are the credits a series keeps: the first MaxCast of its
// cast in billing order, each with the role they play in the most
// episodes; then the directors, then its creators and the writers, the
// crew by how many episodes they worked on, most first.
func seriesCredits(s tmdb.Series, c tmdb.AggregateCredits) []db.CreditIn {
	cast := slices.Clone(c.Cast)
	slices.SortStableFunc(cast, func(a, b tmdb.SeriesCast) int { return cmp.Compare(a.Order, b.Order) })
	var out []db.CreditIn
	seen := map[int]bool{}
	for _, p := range cast {
		if len(out) == MaxCast {
			break
		}
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		role, most := "", -1
		for _, r := range p.Roles {
			if r.Episodes > most && strings.TrimSpace(r.Character) != "" {
				role, most = strings.TrimSpace(r.Character), r.Episodes
			}
		}
		out = append(out, credit(p.Person, "cast", role, "", p.Episodes))
	}
	type crew struct {
		person   tmdb.Person
		jobs     []string
		episodes int
	}
	pick := func(keep func(dept, job string) bool) []crew {
		var list []crew
		for _, p := range c.Crew {
			var jobs []string
			n := 0
			for _, j := range p.Jobs {
				if keep(p.Department, j.Job) {
					jobs = append(jobs, strings.TrimSpace(j.Job))
					n += j.Episodes
				}
			}
			if len(jobs) > 0 {
				list = append(list, crew{p.Person, jobs, n})
			}
		}
		slices.SortStableFunc(list, func(a, b crew) int { return cmp.Compare(b.episodes, a.episodes) })
		return list
	}
	for _, p := range pick(func(_, job string) bool { return job == "Director" }) {
		out = append(out, credit(p.person, "crew", strings.Join(p.jobs, ", "), "Directing", p.episodes))
	}
	creators := map[int]bool{}
	for _, p := range s.CreatedBy {
		if creators[p.ID] {
			continue
		}
		creators[p.ID] = true
		out = append(out, credit(p, "crew", "Creator", "Writing", 0))
	}
	for _, p := range pick(func(dept, _ string) bool { return dept == "Writing" }) {
		if !creators[p.person.ID] {
			out = append(out, credit(p.person, "crew", strings.Join(p.jobs, ", "), "Writing", p.episodes))
		}
	}
	return out
}

// credit is one credit of a person as the server keeps it.
func credit(p tmdb.Person, kind, role, dept string, episodes int) db.CreditIn {
	c := db.CreditIn{ProviderID: strconv.Itoa(p.ID), Name: strings.TrimSpace(p.Name), KnownFor: p.KnownFor,
		Kind: kind, Role: role, Department: dept, Episodes: episodes}
	if p.ProfilePath != "" {
		c.PhotoRef, c.PhotoKey = p.ProfilePath, providerKey("tmdb", p.ProfilePath)
	}
	return c
}
