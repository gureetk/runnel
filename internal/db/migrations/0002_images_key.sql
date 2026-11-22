-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Migration 0002: an index on images.key, by which GET /img finds an image
-- (docs/03-data-model.md, section 3).

CREATE INDEX images_key ON images(key);
