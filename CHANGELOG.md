# Changelog

## [1.0.4](https://github.com/anatolykoptev/go-product-search/compare/v1.0.3...v1.0.4) (2026-09-25)


### Fixed

* **sources:** surface slickdeals card data, AND-match shopify queries ([#23](https://github.com/anatolykoptev/go-product-search/issues/23)) ([8b56387](https://github.com/anatolykoptev/go-product-search/commit/8b563877a80239787e68aac3bb4bab60ad5709b6))

## [1.0.3](https://github.com/anatolykoptev/go-product-search/compare/v1.0.2...v1.0.3) (2026-09-25)


### Fixed

* **extract:** handle schema.org ProductGroup variant pages ([#21](https://github.com/anatolykoptev/go-product-search/issues/21)) ([2c11b6a](https://github.com/anatolykoptev/go-product-search/commit/2c11b6ac6bc07cdaa15dd00b36a7a12011da3d13))

## [1.0.2](https://github.com/anatolykoptev/go-product-search/compare/v1.0.1...v1.0.2) (2026-09-25)


### Fixed

* **extract:** render tier waits domcontentloaded with doubled timeout ([#18](https://github.com/anatolykoptev/go-product-search/issues/18)) ([73efce1](https://github.com/anatolykoptev/go-product-search/commit/73efce104e13564fef654772f70afd1ca2adc49b))

## [1.0.1](https://github.com/anatolykoptev/go-product-search/compare/v1.0.0...v1.0.1) (2026-09-25)


### Fixed

* **extract:** escalate CF-signature fetch errors to render tier ([#16](https://github.com/anatolykoptev/go-product-search/issues/16)) ([651beb2](https://github.com/anatolykoptev/go-product-search/commit/651beb2bc52f93d5c106676036295ca055f3951b))

## 1.0.0 (2026-09-25)


### Added

* candidate sourcing stage — marketplace adapters + merge/dedup funnel (p2) ([2747757](https://github.com/anatolykoptev/go-product-search/commit/2747757b371db713e6bd1eb3b5fd805752bd4995))
* extraction+normalization stage — schema.org parse, fenced LLM fallback, extraction cache (p3) ([1948d2f](https://github.com/anatolykoptev/go-product-search/commit/1948d2f2d10f9752a8ce62630c1ec36b07ae29b4))
* jeff match stage — criteria planner, deterministic prefilter, packed noul Ask, loud degrade (p4) ([867a553](https://github.com/anatolykoptev/go-product-search/commit/867a5532650cda770dcec831bfe5a583d453d50c))
* P6 cost + resilience hardening — budgets, pacing, render tier, feedback sink ([5793f6c](https://github.com/anatolykoptev/go-product-search/commit/5793f6c79ec2b96f6a9003d0e2101b78de8bcea2))
* P7 calibration ids + acceptance probes — request_id pairing, product_probe, runbook ([f2dd19a](https://github.com/anatolykoptev/go-product-search/commit/f2dd19aafbb91d955c5e2165d42e2df7c0038e4c))
* rank fusion + MCP tool surface — product_search/product_match, public projection (p5) ([8cd3d04](https://github.com/anatolykoptev/go-product-search/commit/8cd3d04ea776ea441d3c948fc5d000b2cc4e0367))
* scaffold go-product-search service (P1) ([72534ea](https://github.com/anatolykoptev/go-product-search/commit/72534eac47c964d1764e160244f64bb06cecccac))
* service scaffold — MCP+REST server, bearer auth, config, deploy wiring ([e31458e](https://github.com/anatolykoptev/go-product-search/commit/e31458e4165bb8550ca0e81e9293231290674a5b))


### Fixed

* **match:** review findings — timeout propagation, UTF-8 boundary, NaN bounds, noul clamp, degrade-metric hygiene ([f9df098](https://github.com/anatolykoptev/go-product-search/commit/f9df0980e6dc89f9434cc16696d5064ef68af20f))
* **p6:** review nits — feedback tool not read-only, nil-response guard in fetchgate ([c66d964](https://github.com/anatolykoptev/go-product-search/commit/c66d96413e2d5e8fbbdaf2a9dc64d1318d9fe1eb))
