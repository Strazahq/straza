-- The statements of the sales team. tools.yaml in this folder makes each
-- one a tool.

-- name: revenue_by_month
SELECT date_trunc('month', ordered_on)::date AS month, region, currency, sum(net_amount) AS net_revenue
FROM sales.orders
WHERE ordered_on >= $1::date
  AND status <> 'cancelled'
GROUP BY 1, 2, 3
ORDER BY 1, 2;
