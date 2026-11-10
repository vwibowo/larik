# Larik v0.35.0

_November 10, 2026 · Previous release: v0.34.0._

Database Manager connections can now use SSL/TLS settings and passwords entered directly in the connection form. Direct passwords are kept in the operating system credential store.

### Added

- **PostgreSQL SSL mode.** Choose `disable`, `allow`, `prefer`, `require`, `verify-ca`, or `verify-full`; the default is `prefer`.
- **MySQL/MariaDB TLS mode.** Configure TLS from the connection form, with `required` as the default. Existing DSN environment-variable profiles continue to work.
- **Direct password entry.** Enter passwords for PostgreSQL, MySQL/MariaDB, and SQL Server connections in the form. Passwords are stored in the operating system credential store, never in the settings file. Leave the field blank while editing to keep a saved password, or remove it explicitly. Environment-variable credentials remain available.

### Compatibility

- Existing environment-backed PostgreSQL, MySQL/MariaDB, and SQL Server connections continue to load. If the system credential store is unavailable, environment-variable credentials remain an option.
