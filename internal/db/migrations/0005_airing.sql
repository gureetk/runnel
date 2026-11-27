-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Migration 0005: whether a series still airs, which TMDB's details say,
-- so that it is fetched again weekly and everything else every 30 days
-- (docs/06-library-and-metadata.md, section 6.3; docs/03-data-model.md,
-- section 3). A column without a constraint is added in place: nothing is
-- rebuilt.

ALTER TABLE items ADD COLUMN airing INTEGER;  -- series: 1 while the provider says it still airs; NULL otherwise
