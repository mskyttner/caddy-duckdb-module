# SUHF Guidance for Evaluative Bibliometrics in Sweden (2025)

Source: SUHF working group for bibliometrics (Jenny Samuelsson et al.), August 2025.
Complements: DORA, CoARA/ARRA, Leiden Manifesto, Barcelona Declaration on Open Research Information.

This guidance applies when helping users write bibliometric queries against OpenAlex-derived
data (e.g. the swemetrics endpoint). Apply these recommendations proactively.

---

## 12 Recommendations

### Section 1 — Model design

**Rec 1.** Define the institutional goals *before* building the model. Be aware of gaming risk
(cf. Leiden Manifesto principles 2 and 9).

**Rec 2.** Goals evaluated by bibliometrics should be mutually consistent. Different indicators
may behave differently across research fields.

**Rec 3.** Use multiple indicators rather than a single composite score. Prefer
trend-over-time models over point-in-time snapshots.

**Rec 4.** Bibliometrics always *complements* peer review — it never replaces it
(Leiden Manifesto principle 1).

### Section 2 — Data quality, openness, methods

**Rec 5.** Document data sources and make them available to the people being evaluated.
Prefer open sources (Barcelona Declaration on Open Research Information).

**Rec 6.** Know the coverage and quality of your data source. As a rule of thumb, units with
fewer than **50 publications** should be interpreted with caution — flag this explicitly.
Citation data is right-skewed: **use percentiles and medians, not means**.

**Rec 7.** Document when and how the analysis was run, how publications were identified,
and what the limitations are.

**Rec 8.** Use scientifically supported methods; test before deployment. Open data sources
enable reproducibility.

**Rec 9.** Normalise for field differences. **JIF (Journal Impact Factor) and h-index must
not be used for cross-field comparisons.** Use field-normalised indicators such as:

- `fwci` (Field-Weighted Citation Impact, pre-computed by OpenAlex)
- `citation_normalized_percentile` (top-10 / top-1 percentile bands)
- `works_citation_normalized_percentile.value` (0–1 continuous percentile)

### Section 3 — Results and interpretation

**Rec 10.** Always state the publication volume (`COUNT(*)`) underlying the analysis. Small
volumes should trigger an explicit caveat.

**Rec 11.** Avoid false precision — do not report excessive decimal places; always report
potential error sources and confidence limitations.

**Rec 12.** Do not compare results that are not directly comparable (different actors,
different time windows, different methods). Note any such incomparability explicitly.

---

## swemetrics column mapping

| Recommendation | Preferred column(s) | Avoid |
|---|---|---|
| Rec 6, 9 | `fwci`, `citation_normalized_percentile` | raw `cited_by_count` alone |
| Rec 6 | percentile aggregations (`q50`, `q75`) | `AVG(cited_by_count)` |
| Rec 6 | `COUNT(*) FILTER (...)` to check n ≥ 50 | — |
| Rec 10 | always include `COUNT(*)` alongside indicators | — |
| Rec 12 | note time-window differences in fwci trend queries | — |

Key tables on the swemetrics endpoint:

- `works` — `fwci`, `publication_year`, `cited_by_count`, `doi`
- `works_citation_normalized_percentile` — `is_in_top_1_percent`, `is_in_top_10_percent`, `value`
- `works_open_access` — `oa_status`
- `works_primary_location` — `source_is_core` (peer-reviewed journal subset)
- `works_primary_topic` — `topic_id` (link to FORD/subfield crosswalk)
- `works_sdgs` — `sdg_id`, `score` (use score ≥ 0.4 to reduce noise)
- `authors`, `institutions`, `sources` — entity metadata

---

## Query patterns for responsible bibliometrics

### Always include publication volume

```sql
SELECT
    publication_year,
    COUNT(*)                                           AS n_works,
    ROUND(AVG(fwci), 2)                               AS mean_fwci,
    ROUND(MEDIAN(fwci), 2)                            AS median_fwci,
    ROUND(100.0 * SUM(is_in_top_10_percent::int)
          / NULLIF(COUNT(*), 0), 1)                   AS pct_top10
FROM works w
JOIN works_citation_normalized_percentile p USING (work_id)
WHERE publication_year BETWEEN 2018 AND 2023
GROUP BY ALL
ORDER BY publication_year;
```

### Flag small-n units

```sql
SELECT
    field_display_name,
    COUNT(*)                        AS n_works,
    ROUND(AVG(fwci), 2)            AS mean_fwci,
    CASE WHEN COUNT(*) < 50
         THEN '⚠ n < 50 — interpret with caution'
         ELSE '' END                AS caution
FROM works w
JOIN works_primary_topic t USING (work_id)
WHERE publication_year = 2022
GROUP BY ALL
HAVING COUNT(*) > 0
ORDER BY n_works DESC;
```

### Core (bibliometric universe) filter

Restrict citation indicators to peer-reviewed journal articles:

```sql
WHERE source_is_core = true
```

This corresponds to the "bibliometric universe" used by most national and international
evaluations, excluding conference papers, book chapters, and grey literature.

---

## Key signals for LLM query planning

- `spill_to_disk_bytes > 0` in `server_status` → previous query overflowed RAM; simplify next query
- `n < 50` in result → add explicit caution note in your response
- User asks for `AVG(cited_by_count)` → suggest `MEDIAN` or top-percentile share instead
- User asks to compare fwci across fields → remind them fwci is already field-normalised, but
  cross-field fwci comparisons are still discouraged for formal evaluation purposes
- User mentions JIF or h-index for cross-field ranking → apply Rec 9 and suggest alternatives
