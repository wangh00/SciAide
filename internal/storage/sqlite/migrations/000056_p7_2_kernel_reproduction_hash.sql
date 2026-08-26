ALTER TABLE python_kernel_executions
ADD COLUMN reproduction_sha256 TEXT NOT NULL DEFAULT '';
