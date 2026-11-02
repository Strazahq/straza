-- The statements of the helpdesk team. Each -- name: line starts one
-- statement and names its tool, and tools.yaml in this folder makes it one.

-- name: tickets_by_status
SELECT status, count(*) AS tickets
FROM public.tickets
WHERE opened_on >= $1
GROUP BY status
ORDER BY status;

-- name: close_stale_tickets
UPDATE public.tickets
SET status = 'closed', updated_on = current_date
WHERE status = 'pending'
  AND updated_on < current_date - $1::int4
RETURNING id;
