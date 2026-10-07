# ggsql Syntax Reference

[ggsql](https://github.com/posit-dev/ggsql) is a Grammar-of-Graphics extension to SQL: one
query both selects data and describes a chart. This module exposes it two ways:

- **`POST /duckdb/export` with `format=html`** — `sql` must be a ggsql query; renders a
  standalone, self-contained interactive HTML chart via the bundled DuckDB `ggsql` community
  extension (single writer: `html`).
- **`POST /duckdb/ggsql`** — separate `sql` (a plain SELECT) and `visualise` (the
  `VISUALISE ... DRAW ...` clause) fields; proxies to the `ggvisual` sidecar service for many
  more output formats (`vegalite`, `png`, `svg`, `ansi`, `braille`, and more — see `GET
  <ggvisual>/formats`). Requires `ggvisual_service_url` to be configured; returns 503 otherwise.

Syntax below is identical for both paths — only the writer/format selection differs. Verified
against both the bundled DuckDB extension (`ggsql` community extension v0.4.1) and the
standalone `ggsql` CLI (v0.5.2) as of 2026-09-22; no syntax drift found between the two despite
the version gap.

Write queries using ONLY the syntax below — never invent clauses, settings, aesthetics, layer
types, or palette names.

## Query structure

Optional SQL part + required `VISUALISE`/`VISUALIZE` part (both spellings accepted).

- **SELECT → VISUALISE**: last statement is a SELECT (bare, `WITH...SELECT`, set ops); its
  result feeds VISUALISE, which has no `FROM`.
  ```ggsql
  SELECT name, score_a, score_b FROM 'dataset.csv' WHERE value > 50
  VISUALISE score_a AS x, score_b AS y
  [DRAW / PLACE / SCALE / FACET / PROJECT / LABEL clauses]
  ```
- **VISUALISE FROM**: VISUALISE supplies its own source (table/file/CTE/built-in), no trailing
  SELECT.
  ```ggsql
  WITH summary AS (SELECT category, COUNT(*) AS n FROM 'dataset.csv' GROUP BY category)
  VISUALISE category AS x, n AS y FROM summary
  DRAW bar
  ```

**Data sources** (in `VISUALISE ... FROM` or `DRAW ... MAPPING ... FROM`): unquoted table/CTE
(`FROM sales`), quoted file path (`FROM 'data.csv'`), built-in dataset (`FROM ggsql:penguins`,
`FROM ggsql:airquality`). On `/duckdb/ggsql`, `VISUALISE` normally has no `FROM` — the `sql`
field's query result is what's visualized.

## VISUALISE clause

Starts the viz; optional global mappings inherited by every layer.
```
VISUALISE <mapping>, ... FROM <data-source>
```
Mapping forms: explicit `column AS aesthetic` (e.g. `revenue AS y`); implicit `column` (name =
aesthetic name); wildcard `*` (all matching columns); constant `'red' AS fill`.
```ggsql
VISUALISE bill_len AS x, bill_dep AS y, species AS fill FROM ggsql:penguins
VISUALISE * FROM my_table
```

## DRAW clause

Defines a layer; multiple DRAW stack bottom→top. All subclauses optional given global
mappings/data.
```
DRAW <layer-type>
  MAPPING <mapping>, ... FROM <data-source>
  REMAPPING <stat-property> AS <aesthetic>, ...
  SETTING <param> => <value>, ...
  FILTER <condition>
  PARTITION BY <column>, ...
  ORDER BY <column>, ...
```
- **MAPPING** — same forms as VISUALISE; merges with (layer wins over) global mappings, can add
  its own `FROM`. `null` blocks inheriting a global mapping: `MAPPING null AS color`.
- **REMAPPING** — for stat layers (`histogram`, `density`, `boxplot`, `violin`, `smooth`, `bar`
  w/o y): maps a computed stat to an aesthetic, e.g. `REMAPPING density AS y` instead of a
  layer's default stat.
- **SETTING** — literal aesthetic values or layer params (bypasses scales), e.g.
  `SETTING size => 5, stroke => 'red'`. Position adjustment: `'identity'` (default, most
  layers), `'stack'` (default bar/histogram/area), `'dodge'` (default boxplot/violin), `'jitter'`.
- **FILTER** — SQL WHERE condition on layer data: `FILTER sex = 'female' AND body_mass > 4000`.
- **PARTITION BY** — extra grouping columns beyond discrete mappings: `PARTITION BY Month`.
- **ORDER BY** — record order, matters for `path`: `ORDER BY timestamp`.

### Aggregate (a SETTING)

Collapses each group (`PARTITION BY` cols + discrete mappings) to one row, replacing numeric
mappings with aggregated values. Layers: `point line path bar area ribbon range segment rule
text tile` (not stat layers, which have their own).
```ggsql
SETTING aggregate => '<spec>'                -- single
SETTING aggregate => ('<spec>', '<spec>', …) -- list
```
Spec is **untargeted** `'<func>'` (every unmapped-target numeric aesthetic; ≤2 untargeted
defaults — 1st for lower-side aesthetics x/xmin/etc + all non-range layers, 2nd for upper-side
xend/xmax) or **targeted** `'<aes>:<func>'` (overrides untargeted for that aesthetic).

Functions — reductions: `count sum prod min max range mid mean median geomean harmean rms sdev
var iqr se p05–p95`; positional (need upstream `ORDER BY`): `first last diff`; band
`<offset>±[<mult>]<expansion>` e.g. `'mean+1.96sdev'` (offsets: `mean median geomean harmean rms
sum prod min max mid p05–p95`; expansions: `sdev se var iqr range`).

**Explosion**: targeting one aesthetic with multiple functions emits one row/function/group,
tagged by a synthetic `aggregate` column — drive another aesthetic via
`REMAPPING aggregate AS <aes>`. Equal-length exploded aesthetics run in lockstep; single-function
targets repeat each row. Mixed lengths >1 error.
```ggsql
-- min/max envelope as two lines per group, coloured by function
DRAW line MAPPING Date AS x, Temp AS y
  REMAPPING aggregate AS color
  SETTING aggregate => ('y:min', 'y:max')
  PARTITION BY Year
```
**Scale interaction**: for a *targeted* aesthetic, `SCALE BINNED` runs after aggregation (so
stats aren't cancelled within a bin); untargeted `SCALE BINNED` still bins pre-aggregate to drive
grouping. Continuous censoring (`SCALE <aes> FROM (lo, hi)`) and discrete OOB filtering defer to
post-aggregate whenever that aesthetic is aggregated.

## PLACE clause

Annotation layer, literal values only, no data mapping; tuples for multiple annotations.
```ggsql
PLACE point SETTING x => 5, y => 10, color => 'red'
PLACE text SETTING x => (34, 44), y => (66, 49), label => ('Mean = 34', 'Mean = 44')
```

## SCALE clause

Maps data → aesthetic output; sensible defaults always apply. Only `aesthetic` is required.
```
SCALE <type> <aesthetic> FROM <input-range> TO <output-range> VIA <transform>
  SETTING <param> => <value>, ...
  RENAMING <value> => <label>, ...
```
- **Type** (before aesthetic; inferred if omitted): `CONTINUOUS`, `DISCRETE`, `BINNED` (bin
  continuous→discrete, never auto), `ORDINAL` (never auto), `IDENTITY` (pass through, no legend).
- **Aesthetic** — base name only: `x y fill stroke color(=fill+stroke) opacity size linewidth
  linetype shape panel row column`. Position families (xmin/xmax/xend/ymin/ymax/yend) scale via
  base name (`SCALE x ...`).
- **FROM** — continuous `(min, max)`, `null` infers (`(0, null)`); discrete `('A','B','C')` sets
  order & nulls the rest, or include null explicitly.
- **TO** — value array (`('red','blue')`, `(1, 6)`) or named palette (`viridis`, `dark2`,
  `tableau10`).
- **VIA** — continuous: `linear log log2 ln exp10 exp2 exp sqrt square asinh pseudo_log
  pseudo_log2 pseudo_ln integer`; temporal (auto for date/datetime/time cols): `date datetime
  time`; discrete: `string bool`.
- **SETTING** — continuous/binned: `expand` (factor or `(mult,add)`, default 0.05, x/y only),
  `oob` (`'keep'` default x/y, `'censor'` default others, `'squish'`), `breaks` (count/array/
  interval string e.g. `'2 months'`), `pretty` (bool, default true), `reverse` (bool). Continuous
  only: `minor_breaks` (count/array/interval string; ignored by Vega-Lite). Binned only:
  `closed` (`'left'`/`'right'`). Discrete/ordinal: `reverse`.
- **RENAMING** — direct + wildcard formatting (direct wins):
  `RENAMING 'Adelie' => 'Pygoscelis adeliae', 'adelie' => null` or
  `RENAMING * => '{:Title}'` (formatters: `Title UPPER lower`, time `%B %Y`, num `%.1f`).

```ggsql
SCALE x VIA date SETTING breaks => '2 months'
SCALE y FROM (0, 100) SETTING oob => 'squish'
SCALE BINNED x SETTING breaks => 10, pretty => false
```

## FACET clause

Small multiples. 1D `FACET region` (wrap, aesthetic `panel`); 2D `FACET region BY category`
(grid, aesthetics `row`/`column`). Settings: `free` (`null` default/fixed, `'x'`, `'y'`,
`('x','y')`), `missing` (`'repeat'` default / `'null'`), `ncol`/`nrow` (1D only, pick one).
Customize/filter via SCALE on the facet aesthetic:
```ggsql
FACET region
SCALE panel RENAMING 'N' => 'North', 'S' => 'South'
FACET island
SCALE panel FROM ('Biscoe', 'Dream')   -- filters panels shown
```

## PROJECT clause

Coordinate system.
```
PROJECT <aesthetic>, ... TO <coord-type> SETTING <param> => <value>, ...
```
`cartesian` (default): aesthetics x/y; settings `clip` (bool, default true), `ratio` (number or
null). `polar`: aesthetics `radius`(primary)/`angle`(secondary); settings `clip`, `start`/`end`
(degrees, default 0/start+360), `inner` (0-1 donut hole, default 0). Swap order to flip axes
(`PROJECT y, x TO cartesian`); without PROJECT, type is inferred from mappings.
```ggsql
PROJECT TO polar SETTING inner => 0.5   -- donut chart
```

## LABEL clause

Overrides axis/legend labels & titles: `title`, `subtitle`, `caption`, or any aesthetic name;
`null` suppresses.
```ggsql
LABEL title => 'Sales by Region', x => 'Date', y => 'Revenue (USD)', fill => null
```

---

## Layer types

- **point** — required x, y; optional size, colour, stroke, fill, opacity, shape.
- **line** — required x, y; sorted by primary axis; optional colour/stroke, opacity, linewidth,
  linetype; settings `position`, `orientation` (`'aligned'`/`'transposed'`).
- **path** — like line but data-order (unsorted); same aesthetics.
- **bar** — auto-counts if no y; optional x, y, fill, colour, stroke; stats `count`,
  `proportion`; property `weight`; settings `position` (default `'stack'`), `width`. Orientation
  from mapping (x=vertical, y=horizontal).
- **histogram** — required x; stats `count`, `density` (default remap `count AS <secondary>`);
  settings `position` (`'stack'`), `bins` (30), `binwidth`, `closed`.
- **density** — required x; stats `density`, `intensity`; settings `position` (`'identity'`),
  `bandwidth`, `adjust` (1), `kernel` (`'gaussian'` default, `epanechnikov triangular
  rectangular biweight cosine`).
- **boxplot** — required x (cat), y (cont); stats `type`, `value`; settings `position`
  (`'dodge'`), `outliers` (true), `coef` (1.5), `width` (0.9), `hinge` (points, default
  null/hidden).
- **violin** — required x (cat), y (cont); stats `density`, `intensity` (default remap
  `density AS offset`); settings `position` (`'dodge'`), `bandwidth`, `adjust`, `kernel`, `width`
  (0.9), `side` (`'both' 'left' 'bottom' 'right' 'top'`), `tails` (default 3).
- **smooth** — required x, y; stat `intensity`; settings `method` (`'nw'` default, `'ols'`,
  `'tls'`), `bandwidth`, `adjust`, `kernel` (nw only).
- **area** — required x, y, anchored at zero; settings `position` (`'stack'`), `orientation`,
  `total` (normalize), `center` (steamgraph).
- **ribbon** — like area but explicit ymin/ymax, unanchored.
- **segment** — required x, y, xend, yend; use `range` instead when one coord is shared between
  start/end.
- **rule** — required x or y (full-panel reference line); optional `slope` (diagonal:
  `y = a + slope*x`).
- **text** — required x, y, label; settings `offset` (number or `(h,v)`), `format`
  (RENAMING-style interpolation), `parse` (bool, default true: markdown `**bold**`/`*italic*`/
  `~~strike~~`/`` `code` ``/`{.red span}`; not in Vega-Lite), `hjust` (`'left' 'right' 'centre'`
  or 0-1), `vjust` (`'top' 'bottom' 'middle'` or 0-1).
- **rect** — pick 2 per axis from center/min/max/width/height, or just center (defaults size
  to 1).
- **polygon** — required x, y; ordered coords; PARTITION BY separates distinct polygons.
- **range** — required x, ymin, ymax; setting `hinge` (points, default 10, null hides).

All layers accept colour/stroke, fill, opacity, linewidth, linetype, and `position` where
applicable.

## Named color palettes

- Discrete: `ggsql10`(default) `tableau10 category10 set1 set2 set3 dark2 paired pastel1 pastel2
  accent kelly22`
- Sequential: `sequential`(default) `viridis plasma magma inferno cividis blues greens oranges
  reds purples greys ylgnbu ylorbr ylorrd batlow hawaii lajolla turku` …
- Diverging: `vik`/`diverging` `rdbu rdylbu rdylgn spectral brbg prgn piyg puor berlin roma` …
- Cyclic: `romao`/`cyclic` `bamo broco corko viko`

## Common patterns

```ggsql
-- Pie chart: bar layer in polar coords
VISUALISE species AS fill FROM ggsql:penguins
DRAW bar
PROJECT TO polar

-- Multi-series line chart
VISUALISE Date AS x
DRAW line MAPPING Temp AS y, 'Temperature' AS color
DRAW line MAPPING Ozone AS y, 'Ozone' AS color
SCALE x VIA date

-- Lollipop chart
SELECT ROUND(bill_dep) AS bill_dep, COUNT(*) AS n FROM ggsql:penguins GROUP BY 1
VISUALISE bill_dep AS x
DRAW range MAPPING 0 AS ymin, n AS ymax SETTING hinge => null
DRAW point MAPPING n AS y

-- Ridgeline / joy plot
VISUALISE Temp AS x, Month AS y FROM ggsql:airquality
DRAW violin SETTING width => 4, side => 'top'
SCALE ORDINAL y

-- Mean ± 1.96·sdev band per group, as a ribbon
VISUALISE Day AS x, Temp AS ymin, Temp AS ymax FROM ggsql:airquality
DRAW ribbon SETTING aggregate => ('mean-1.96sdev', 'mean+1.96sdev') PARTITION BY Month
```

## Using ggsql via this module

```bash
# format=html: single self-contained chart file, via /duckdb/export
curl -X POST http://localhost:8080/duckdb/export \
  -H "X-API-Key: your-api-key" -H "Content-Type: application/json" \
  -d '{"sql": "FROM users VISUALIZE age AS x, id AS y DRAW bar", "format": "html"}'

# /duckdb/ggsql: SQL and VISUALISE are separate fields; many output formats via ggvisual
curl -X POST http://localhost:8080/duckdb/ggsql \
  -H "X-API-Key: your-api-key" -H "Content-Type: application/json" \
  -d '{"sql": "SELECT age, id FROM users", "visualise": "VISUALISE age AS x, id AS y DRAW bar", "format": "vegalite"}'
```

`/duckdb/ggsql` requires the `ggvisual` sidecar to be configured (`ggvisual_service_url` in the
Caddyfile or `DUCKDB_GGVISUAL_SERVICE_URL`); without it the endpoint returns 503. `ansi`/`braille`
formats return raw terminal escape codes as `text/plain` — never JSON-wrapped, since JSON string
escaping would corrupt them.

### Sizing

Both `/duckdb/ggsql` and the `ggsql_chart` MCP tool accept four optional sizing fields, forwarded
to the `ggvisual` sidecar's own `width`/`height`/`png-width`/`png-height` render params:

| Field | Unit | Applies to | Default | Max (clamped) |
|---|---|---|---|---|
| `width`, `height` | characters | `ansi`, `braille`, `svg`, `braille-svg`, `xterm*`, `braille-xterm*`, `text`, `braille-text`, `cast`, `cast-page` | ggvisual's own per-format default (e.g. `ansi` is 80 cols × 40 rows) | 500 |
| `png_width`, `png_height` | pixels | the intermediate PNG render; also `format=png`'s own output size | 600 × 400 | 4000 |

Omit a field (or pass 0) to use ggvisual's own default for that format. Values above the max are
silently clamped, not rejected; negative values are treated as unset. `vegalite`, `html`, and
other spec-derived formats ignore all four fields — only raster-derived formats are affected.
`format=png` uses `png_width`/`png_height` for its own output size and ignores `width`/`height`.
`ansi`'s aspect-preserving scale mode means the rendered output may not exactly fill the
requested box for a chart whose aspect ratio doesn't match — this is `ggvisual`'s own scaling
behavior, not something this module adjusts for.

```bash
curl -X POST http://localhost:8080/duckdb/ggsql \
  -H "X-API-Key: your-api-key" -H "Content-Type: application/json" \
  -d '{"sql": "SELECT age, id FROM users",
       "visualise": "VISUALISE age AS x, id AS y DRAW bar",
       "format": "braille", "width": 100, "height": 30}'
```

## Reference

https://ggsql.org/syntax/index.llms.md — latest syntax docs (upstream, not embedded here).
