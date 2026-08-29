-- Recipients can ask for the AI insight to be generated automatically and
-- included in "Deployment failed" emails.
ALTER TABLE notification_recipients ADD COLUMN include_insight boolean NOT NULL DEFAULT false;
