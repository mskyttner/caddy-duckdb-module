-- Attach the DiVA OAI normalized database in read-only mode.
-- The file is bind-mounted into the container at /data/diva/.
ATTACH '/data/diva/diva_oai_normalized.db' AS diva (READ_ONLY);

-- Install and load the http_request community extension.
-- Must run before macro creation; macros are created in :memory: (default catalog).
install http_request from community;
load http_request;

-- Load the textplot community extension for ASCII visualizations.
-- Pre-installed in the base image; no INSTALL step needed.
-- Provides tp_bar(), tp_sparkline(), textplot_histogram() etc.
load textplot;

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
                '", "title": "' || memory.json_safe_str(title) ||
                '", "abstract": "' || memory.json_safe_str(abstract) ||
                '", "keywords": "' || memory.json_safe_str(keywords) ||
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

-- Document macros so MCP tool descriptions are meaningful.
COMMENT ON MACRO TABLE api_swepub_classify IS 'classify title and/or abstract according to SSIF/FORD research topic taxonomy. params: level (default ''3''), title (VARCHAR), abstract (VARCHAR), keywords (VARCHAR)';

-- ============================================================================
-- OpenAlex API macros
-- Sourced from: ../demetrius/api_openalex.sql
-- Note: .read is a DuckDB CLI command and does not work through the Go driver,
-- so the macro definitions are inlined here.
-- ============================================================================

create or replace macro url_encode(s) as (
    regexp_replace(
        regexp_replace(
            regexp_replace(
                regexp_replace(
                    regexp_replace(
                        regexp_replace(
                            regexp_replace(
                                regexp_replace(
                                    regexp_replace(
                                        regexp_replace(
                                            regexp_replace(s, ' ', '%20', 'g'),
                                        '\n', '%0A', 'g'),
                                    '"', '%22', 'g'),
                                '\(', '%28', 'g'),
                            '\)', '%29', 'g'),
                        ':', '%3A', 'g'),
                    ',', '%2C', 'g'),
                '\.', '%2E', 'g'),
            '\\', '%5C', 'g'),
        'é', '%C3%A9', 'g'),
    'å', '%C3%A5', 'g')
);

create or replace macro api_search_authors(searchterm) as table (
    from read_json(concat(
        'https://api.openalex.org/autocomplete/authors?',
        'mailto=support@openalex.org&',
        'q=', url_encode(searchterm)
    )),
    unnest(results) as _(r)
    select
        author_id: replace(r.id, 'https://openalex.org/A', '')::bigint,
        orcid: coalesce(replace(r.external_id, 'https://orcid.org/', ''), ''),
        r.display_name,
        r.hint,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
);

create or replace macro api_search_works(searchterm) as table (
    from read_json(concat(
        'https://api.openalex.org/autocomplete/works?',
        'mailto=support@openalex.org&',
        'q=', url_encode(searchterm)
    )),
    unnest(results) as _(r)
    select
        work_id: replace(r.id, 'https://openalex.org/W', '')::bigint,
        doi: coalesce(replace(r.external_id, 'https://doi.org/', ''), ''),
        r.display_name,
        r.hint,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
);

create or replace macro api_search_sources(searchterm) as table (
    from read_json(concat(
        'https://api.openalex.org/autocomplete/sources?',
        'mailto=support@openalex.org&',
        'q=', url_encode(searchterm)
    )),
    unnest(results) as _(r)
    select
        source_id: replace(r.id, 'https://openalex.org/S', '')::bigint,
        r.display_name,
        r.hint,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
        r.external_id
);

create or replace macro api_search_institutions(searchterm) as table (
    from read_json(concat(
        'https://api.openalex.org/autocomplete/institutions?',
        'mailto=support@openalex.org&',
        'q=', url_encode(searchterm)
    )),
    unnest(results) as _(r)
    select
        institution_id: replace(r.id, 'https://openalex.org/I', '')::bigint,
        r.display_name,
        r.hint,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
        ror: coalesce(replace(r.external_id, 'https://ror.org/', ''), ''),
);

create or replace macro api_search_publishers(searchterm) as table (
    from read_json(concat(
        'https://api.openalex.org/autocomplete/publishers?',
        'mailto=support@openalex.org&',
        'q=', url_encode(searchterm)
    )),
    unnest(results) as _(r)
    select
        publisher_id: replace(r.id, 'https://openalex.org/P', '')::bigint,
        r.display_name,
        hint: r.hint::varchar,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
        wikidata_url: r.external_id
);

create or replace macro api_search_funders(searchterm) as table (
    from read_json(concat(
        'https://api.openalex.org/autocomplete/funders?',
        'mailto=support@openalex.org&',
        'q=', url_encode(searchterm)
    )),
    unnest(results) as _(r)
    select
        funder_id: replace(r.id, 'https://openalex.org/F', '')::bigint,
        r.display_name,
        hint: r.hint::varchar,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
        ror: coalesce(replace(r.external_id, 'https://ror.org/', ''), NULL),
);

create or replace macro api_search_topics(searchterm) as table (
    from read_json(concat(
        'https://api.openalex.org/autocomplete/topics?',
        'mailto=support@openalex.org&',
        'q=', url_encode(searchterm)
    )),
    unnest(results) as _(r)
    select
        topic_id: replace(r.id, 'https://openalex.org/T', '')::int,
        r.display_name,
        hint: r.hint::varchar,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
        wikipedia_url: r.external_id,
);

create or replace macro api_search_id(searchterm) as table (
    from read_json(concat(
        'https://api.openalex.org/autocomplete?',
        'mailto=support@openalex.org&',
        'q=', url_encode(searchterm)
    )),
    unnest(results) as _(r)
    select
        id: regexp_replace(r.id, 'https://openalex.org/.', '')::bigint,
        r.entity_type,
        r.display_name,
        r.hint,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
        r.external_id,
);

create or replace macro api_search_orcid(orcid) as table (
    from read_json(concat(
        'https://api.openalex.org/autocomplete?',
        'mailto=support@openalex.org&',
        'q=https://orcid.org/', orcid
    )),
    unnest(results) as _(r)
    select
        author_id: replace(r.id, 'https://openalex.org/A', '')::bigint,
        orcid: replace(r.external_id, 'https://orcid.org/', ''),
        r.display_name,
        hint: r.hint::varchar,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
);

create or replace macro api_search_doi(doi) as table (
    from read_json(concat(
        'https://api.openalex.org/autocomplete?',
        'mailto=support@openalex.org&',
        'q=https://doi.org/', doi
    )),
    unnest(results) as _(r)
    select
        work_id: replace(r.id, 'https://openalex.org/W', '')::bigint,
        doi: replace(r.external_id, 'https://doi.org/', ''),
        r.display_name,
        hint: r.hint::varchar,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
);

create or replace macro api_works_filter(filter_string, per_page := 25, page := 1) as table (
    from read_json(concat(
        'https://api.openalex.org/works?',
        'mailto=support@openalex.org&',
        'filter=', url_encode(filter_string), '&',
        'per-page=', per_page, '&',
        'page=', page
    )),
    unnest(results) as _(r)
    select
        work_id: replace(r.id, 'https://openalex.org/W', '')::bigint,
        doi: coalesce(replace(r.doi, 'https://doi.org/', ''), ''),
        r.display_name,
        publication_year: r.publication_year::int,
        cited_by_count: r.cited_by_count::int,
        r.type,
        page_number: page,
);

create or replace macro api_authors_filter(filter_string, per_page := 25, page := 1) as table (
    from read_json(concat(
        'https://api.openalex.org/authors?',
        'mailto=support@openalex.org&',
        'filter=', url_encode(filter_string), '&',
        'per-page=', per_page, '&',
        'page=', page
    )),
    unnest(results) as _(r)
    select
        author_id: replace(r.id, 'https://openalex.org/A', '')::bigint,
        orcid: coalesce(replace(r.orcid, 'https://orcid.org/', ''), ''),
        r.display_name,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
        page_number: page,
);

create or replace macro api_works_search(searchterms, per_page := 25) as table (
    from read_json(concat(
        'https://api.openalex.org/works?',
        'mailto=support@openalex.org&',
        'search=', url_encode(searchterms), '&',
        'per-page=', per_page
    )),
    unnest(results) as _(r)
    select
        work_id: replace(r.id, 'https://openalex.org/W', '')::bigint,
        doi: coalesce(replace(r.doi, 'https://doi.org/', ''), ''),
        r.display_name,
        publication_year: r.publication_year::int,
        cited_by_count: r.cited_by_count::int,
        r.type,
);

create or replace macro api_authors_search(searchterms, per_page := 25) as table (
    from read_json(concat(
        'https://api.openalex.org/authors?',
        'mailto=support@openalex.org&',
        'filter=', url_encode(searchterms), '&',
        'per-page=', per_page
    )),
    unnest(results) as _(r)
    select
        author_id: replace(r.id, 'https://openalex.org/A', '')::bigint,
        orcid: coalesce(replace(r.orcid, 'https://orcid.org/', ''), ''),
        r.display_name,
        cited_by_count: r.cited_by_count::int,
        works_count: r.works_count::int,
);

create or replace macro api_works_search_sorted(
    searchterms,
    sort := 'cited_by_count:desc',
    per_page := 25
) as table (
    from read_json(concat(
        'https://api.openalex.org/works?',
        'mailto=support@openalex.org&',
        'search=', url_encode(searchterms), '&',
        'sort=', sort, '&',
        'per-page=', per_page
    )),
    unnest(results) as _(r)
    select
        work_id: replace(r.id, 'https://openalex.org/W', '')::bigint,
        doi: coalesce(replace(r.doi, 'https://doi.org/', ''), ''),
        r.display_name,
        publication_year: r.publication_year::int,
        cited_by_count: r.cited_by_count::int,
        r.type,
);

create or replace macro api_aboutness_topics(title) as table (
    from read_json(concat(
        'https://api.openalex.org/text/topics?',
        'mailto=support@openalex.org&',
        'title=', url_encode(title)
    )),
    unnest(topics) _(t)
    select
        topic_id: replace(t.id, 'https://openalex.org/T', '')::int,
        score: t.score,
        subfield_id: replace(t.subfield.id, 'https://openalex.org/subfields/', '')::int,
        field_id: replace(t.field.id, 'https://openalex.org/fields/', '')::int,
        domain_id: replace(t.domain.id, 'https://openalex.org/domains/', '')::int,
    order by score desc
);

create or replace macro api_works_groupby_year(filter_string) as table (
    from read_json(concat(
        'https://api.openalex.org/works?',
        'mailto=support@openalex.org&',
        'filter=', url_encode(filter_string), '&',
        'group_by=publication_year'
    )),
    unnest(group_by) as _(g)
    select
        year: g.key::int,
        count: g.count::bigint,
);

create or replace macro api_works_groupby_field(filter_string, field_name) as table (
    from read_json(concat(
        'https://api.openalex.org/works?',
        'mailto=support@openalex.org&',
        'filter=', url_encode(filter_string), '&',
        'group_by=', field_name
    )),
    unnest(group_by) as _(g)
    select
        key: g.key,
        key_display_name: g.key_display_name,
        count: g.count::bigint,
);

create or replace macro api_get_works_bulk(work_ids) as table (
    from read_json(concat(
        'https://api.openalex.org/works?',
        'mailto=support@openalex.org&',
        'filter=ids.openalex:',
        regexp_replace(work_ids, ',', '|', 'g')
    )),
    unnest(results) as _(r)
    select
        work_id: replace(r.id, 'https://openalex.org/W', '')::bigint,
        doi: coalesce(replace(r.doi, 'https://doi.org/', ''), ''),
        r.display_name,
        publication_year: r.publication_year::int,
        cited_by_count: r.cited_by_count::int,
);

-- Switch default catalog to diva so unqualified table names resolve to diva tables.
-- Macros created above remain in :memory: and are still callable from any catalog.
USE diva;
