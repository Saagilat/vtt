ALTER TABLE chat_messages ADD COLUMN to_account_id TEXT REFERENCES accounts(id) ON DELETE CASCADE;
