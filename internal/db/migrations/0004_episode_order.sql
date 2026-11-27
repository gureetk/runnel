-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Migration 0004: the place of an episode without a number in its season
-- (D51; docs/03-data-model.md, section 3). A column without a constraint
-- is added in place: nothing is rebuilt.

ALTER TABLE items ADD COLUMN episode_order REAL;  -- episodes without a number: a decimal's value, such as 8.5; NULL otherwise
