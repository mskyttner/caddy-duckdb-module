-- Attach the DiVA OAI normalized database in read-only mode.
-- The file is bind-mounted into the container at /data/diva/.
ATTACH '/data/diva/diva_oai_normalized.db' AS diva (READ_ONLY);

-- Install and load the http_request community extension.
-- Must run before macro creation; macros are created in :memory: (default catalog).
install http_request from community;
load http_request;

-- Helper macro to sanitize text for JSON in BLOB (removes problematic chars).
-- Note: Backslashes are removed entirely to avoid BLOB escape sequence issues.
create or replace macro json_safe_str(txt) as
    regexp_replace(
        regexp_replace(
            replace(
                replace(
                    replace(
                        replace(
                            coalesce(txt::varchar, ''),
                            '\', ''
                        ),
                        '"', ''''
                    ),
                    chr(10), ' '
                ),
                chr(13), ' '
            ),
            '[\x00-\x1F]', ' ', 'g'
        ),
        '[^\x20-\x7E]', '', 'g'
    );

-- Table macro: classify a publication using the Swepub HSV classification API.
-- Returns: score, code, eng_label, swe_label
-- Example: FROM api_swepub_classify(title := 'Magnetic resonance');
create or replace macro api_swepub_classify(level := '3', title := NULL, abstract := NULL, keywords := NULL) as table (

    with classification as (
        from http_post(
            'https://bibliometri.swepub.kb.se/api/v2/classify/',
            body := ('{"level": "' || coalesce(level::varchar, '3') ||
                '", "title": "' || json_safe_str(title) ||
                '", "abstract": "' || json_safe_str(abstract) ||
                '", "keywords": "' || json_safe_str(keywords) ||
                '"}')::BLOB,
            "content_type" := 'application/json'
        )
        select
            j: decode(body)::json,
    ),

    resp as (
        from classification
        select
            abstract: j->'$.abstract',
            match_status: j->'$.status',
            suggestions: unnest(json_transform(j->'$.suggestions', '[ {"@id":"VARCHAR","@type":"VARCHAR","_score":"DOUBLE","broader":{"@id":"VARCHAR","@type":"VARCHAR","code":"VARCHAR","inScheme":{"@id":"VARCHAR"},"prefLabelByLang":{"en":"VARCHAR","sv":"VARCHAR"},"topConceptOf":{"@id":"VARCHAR"}},"code":"VARCHAR","inScheme":{"@id":"VARCHAR"},"prefLabelByLang":{"en":"VARCHAR","sv":"VARCHAR"}} ]')),
            json_structure(suggestions),
    )

    from resp
    select
        score: suggestions."_score",
        code: suggestions."code",
        eng_label: suggestions."prefLabelByLang".en,
        swe_label: suggestions."prefLabelByLang".sv,
);

-- Switch default catalog to diva so unqualified table names resolve to diva tables.
-- Macros created above remain in :memory: and are still callable from any catalog.
USE diva;
