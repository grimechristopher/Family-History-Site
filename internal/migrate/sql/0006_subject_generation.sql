-- How far back a subject is from the people being asked: 0 is a contributor
-- themselves, 1 their parents, 2 grandparents, 3 great-grandparents.
--
-- The tree derivation has always known this; it was simply never stored, because
-- nothing needed it until the sidebar grew long enough to want grouping. A list of
-- two dozen names is easier to use as four short ones.
ALTER TABLE family.subjects ADD COLUMN generation int NOT NULL DEFAULT 0;
