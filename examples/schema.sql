-- Entirely synthetic schema; no identity, defaults or foreign keys.
-- The following marker is required by the existing legacy DDL parser.
-- V80.dbo.sample_items definition
CREATE TABLE [dbo].[sample_items] (
    [id] int NOT NULL,
    [label] nvarchar(40) NULL,
    CONSTRAINT [PK_sample_items] PRIMARY KEY ([id])
);
