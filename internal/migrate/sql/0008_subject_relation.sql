-- How a subject reaches the roots: on the line of descent, or beside it.
--
-- An ancestor chart is a line, so it leaves out exactly the people a family talks
-- about most: somebody's sister, their aunt, the cousins they grew up with. Those
-- are subjects now, and this says which heading they belong under -- calling an
-- aunt a grandparent because she is one generation up would be worse than not
-- having her at all.
ALTER TABLE family.subjects ADD COLUMN relation text NOT NULL DEFAULT 'ancestor';
