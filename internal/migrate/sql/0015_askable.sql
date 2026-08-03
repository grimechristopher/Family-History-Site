-- Whether somebody can be asked a question, independent of whether they run the
-- line. Role decides who may manage a family; askable decides who is offered as
-- an answer to "who should answer it?" -- normally the same as being a
-- contributor, except an admin who is also a family member with memories worth
-- asking for can be made askable by hand.
ALTER TABLE core.family_members ADD COLUMN askable boolean NOT NULL DEFAULT true;
UPDATE core.family_members SET askable = (role <> 'admin');
