-- Supports the membership foreign-key action that unassigns tasks on removal.
CREATE INDEX idx_tasks_assignee_org
    ON tasks (assignee_id, organization_id);