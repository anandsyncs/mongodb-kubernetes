# MCK 3.0.0 Release Notes

This is a new major release of the MongoDB Kubernetes Operator (MCK) with significant changes and improvements.

## Breaking Changes

* **MongoDB**, **MongoDBMulti**: Combined both resources into single **MongoDB** resource.

## New Features

* **MongoDB**: public search preview release of MongoDB Search (Community Edition) is now available.
    * Added new property [spec.search](https://www.mongodb.com/docs/kubernetes/current/mongodb/specification/#spec-search) to enable MongoDB Search.

## Bug Fixes

* Fixes the bug when status of `MongoDBUser` was being set to `Updated` prematurely. For example, new users were not immediately usable following `MongoDBUser` creation despite the operator reporting `Updated` state.
