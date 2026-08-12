-- Pipeline stage label linking table.
--
-- Maps pipeline stage numbers to the entity's label UUID so the engine can
-- emit label_id in pg_notify payloads without ever touching label names.
-- Labels themselves live in the labels table (entity-scoped, localizable).
--
-- Stage assignments:
--   0  → Importing      (transactions written to DB)
--   2  → Categorizing   (entry matching + pattern detection complete)
--   6  → Analyzing      (day-crawl + snapshots written)
--   7  → Forecasting    (projections written)

CREATE TABLE pipeline_stage_labels (
    stage_num  INTEGER NOT NULL,
    entity_id  UUID    NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    label_id   UUID    NOT NULL REFERENCES labels(id)   ON DELETE CASCADE,
    PRIMARY KEY (stage_num, entity_id)
);

CREATE INDEX ON pipeline_stage_labels (entity_id);

-- Backfill: seed stage labels for all existing entities and link them.
-- Uses a multi-row CTE so the insert + link happen atomically per entity.
WITH seeded AS (
    INSERT INTO labels (id, entity_id, name, source, created_at)
    SELECT gen_random_uuid(), e.id, v.name, 'system', NOW()
    FROM entities e
    CROSS JOIN (VALUES
        ('Importing'),
        ('Categorizing'),
        ('Analyzing'),
        ('Forecasting')
    ) AS v(name)
    ON CONFLICT (entity_id, name) DO UPDATE SET source = 'system'
    RETURNING id, entity_id, name
)
INSERT INTO pipeline_stage_labels (stage_num, entity_id, label_id)
SELECT
    CASE s.name
        WHEN 'Importing'    THEN 0
        WHEN 'Categorizing' THEN 2
        WHEN 'Analyzing'    THEN 6
        WHEN 'Forecasting'  THEN 7
    END,
    s.entity_id,
    s.id
FROM seeded s
ON CONFLICT (stage_num, entity_id) DO UPDATE SET label_id = EXCLUDED.label_id;
