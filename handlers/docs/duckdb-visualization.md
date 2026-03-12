# DuckDB Visualization Query Patterns

Query patterns for producing data suitable for common chart types.

---

## Time Series (Line Chart)

Suitable for: numeric values with a timestamp or date column — trends, seasonality, anomalies.

```sql
SELECT
    date_trunc('day', ts) AS date,
    avg(metric)           AS avg_value
FROM my_table
WHERE ts BETWEEN '2024-01-01' AND '2024-12-31'
GROUP BY ALL
ORDER BY date;
```

Best practices:
- Choose granularity to match the data density (`'hour'`, `'day'`, `'month'`)
- Use `date_trunc()` for consistent bucketing
- Filter to a relevant time window to avoid sparse endpoints

---

## Bar Chart

Suitable for: categorical column with a numeric measure — comparisons, rankings.

```sql
SELECT
    category,
    sum(amount) AS total
FROM my_table
GROUP BY category
ORDER BY total DESC
LIMIT 10;
```

Best practices:
- Limit to top N to avoid cluttered output
- Use `SUM`, `AVG`, or `COUNT` depending on what you want to compare
- For long category names, horizontal bars render better

---

## Scatter Plot

Suitable for: two or more numeric columns — correlations, clusters, outliers.

```sql
SELECT
    numeric_col1,
    numeric_col2,
    category_col   -- optional: color dimension
FROM my_table
WHERE numeric_col1 IS NOT NULL
  AND numeric_col2 IS NOT NULL
LIMIT 1000;
```

Best practices:
- Filter nulls explicitly
- Limit to ~1000 points for rendering performance
- Add a third categorical column for color grouping

---

## Heatmap

Suitable for: two categorical dimensions with a numeric measure — frequency matrices, cross-tabulations.

```sql
SELECT
    cat1,
    cat2,
    count(*) AS frequency
FROM my_table
GROUP BY cat1, cat2
ORDER BY cat1, cat2;
```

Best practices:
- Use log scale for skewed distributions
- Sort axes meaningfully (e.g. by total, alphabetically, or by time)

---

## ASCII Text Plots (textplot extension)

The `textplot` community extension renders charts as VARCHAR strings returned inline in query results — no external renderer needed. Output is readable in any terminal, markdown block, or chat interface.

Load once per session (or in init.sql):
```sql
INSTALL textplot FROM community;
LOAD textplot;
```

### Horizontal Bar Chart (`tp_bar`)

Suitable for: comparing a numeric measure across categories, directly in the result set.

```sql
SELECT
    category,
    count(*) AS n,
    tp_bar(count(*), min := 0, max := 200, width := 40) AS chart
FROM my_table
GROUP BY category
ORDER BY n DESC
LIMIT 15;
```

### Sparkline (`tp_sparkline`)

Suitable for: compact trend overview — fits a time series into a single cell.

```sql
SELECT
    category,
    tp_sparkline(list(monthly_count ORDER BY month)) AS trend
FROM (
    SELECT
        category,
        date_trunc('month', ts) AS month,
        count(*) AS monthly_count
    FROM my_table
    GROUP BY ALL
)
GROUP BY category
ORDER BY category;
```

### Histogram (`textplot_histogram`)

Suitable for: distribution of a numeric column — shows shape, skew, and outliers.

```sql
SELECT textplot_histogram(metric, bins := 10) AS distribution
FROM my_table
WHERE metric IS NOT NULL;
```

Best practices:
- Output is a VARCHAR column — wrap the query in a CTE if you need it alongside other columns
- `tp_bar` `min`/`max` should match the actual data range for meaningful bar lengths; use a subquery or window function to derive them dynamically if needed
- `tp_sparkline` requires a list — aggregate with `list(val ORDER BY time)` before passing
- Combine `tp_bar` with the raw number for readable output: `category`, `n`, `chart`

---

## Dashboard Composition

For a comprehensive view, combine chart types:
1. Time series — overall trend
2. Bar chart — top categories for the same period
3. Heatmap — breakdown by two dimensions

Use the same `WHERE` clause across all queries for consistent filtering.
