-- Genealogical convention: a woman is recorded under her maiden name but
-- referred to by her married one, written "Given (Maiden) Married".
-- Storing the married surname lets every page render that consistently.
ALTER TABLE family.people ADD COLUMN married_surname text;
