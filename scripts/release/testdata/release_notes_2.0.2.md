# MCK 2.0.2 Release Notes

## Bug Fixes

* Fixed handling proxy environment variables in the operator pod. The environment variables [`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`] when set on the operator pod, can now be propagated to the MongoDB agents by also setting the environment variable `MDB_PROPAGATE_PROXY_ENV=true`.
