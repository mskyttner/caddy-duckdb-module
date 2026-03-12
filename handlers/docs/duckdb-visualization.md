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

## Dashboard Composition

For a comprehensive view, combine chart types:
1. Time series — overall trend
2. Bar chart — top categories for the same period
3. Heatmap — breakdown by two dimensions

Use the same `WHERE` clause across all queries for consistent filtering.
