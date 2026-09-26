# Changelog

## [1.10.0](https://github.com/anatolykoptev/go-product-search/compare/v1.9.0...v1.10.0) (2026-09-26)


### Added

* **extract:** minor-unit money representation ([85ae0ee](https://github.com/anatolykoptev/go-product-search/commit/85ae0ee76905261825f93271ab5239b6da0982a9)), closes [#49](https://github.com/anatolykoptev/go-product-search/issues/49)

## [1.9.0](https://github.com/anatolykoptev/go-product-search/compare/v1.8.0...v1.9.0) (2026-09-26)


### Added

* **match:** typed ReasonCode + exclude_detail on candidates ([e7c429d](https://github.com/anatolykoptev/go-product-search/commit/e7c429dc3c94d89ee0db5e3fd797dc9ba12941ff)), closes [#48](https://github.com/anatolykoptev/go-product-search/issues/48)

## [1.8.0](https://github.com/anatolykoptev/go-product-search/compare/v1.7.0...v1.8.0) (2026-09-26)


### Added

* **sources:** strip affiliate/tracker params off candidate URLs at the funnel boundary ([#60](https://github.com/anatolykoptev/go-product-search/issues/60)) ([3c373ed](https://github.com/anatolykoptev/go-product-search/commit/3c373ede1a4a9dce78b562b630add9b548425f1e))

## [1.7.0](https://github.com/anatolykoptev/go-product-search/compare/v1.6.1...v1.7.0) (2026-09-26)


### Added

* **sources:** Shopify UCP catalog legs — per-shop search_catalog + global catalog ([9f85807](https://github.com/anatolykoptev/go-product-search/commit/9f85807d46afccd57ec4cac6256dc27c4754db20))

## [1.6.1](https://github.com/anatolykoptev/go-product-search/compare/v1.6.0...v1.6.1) (2026-09-26)


### Fixed

* **sources:** interact calls keep pacing but skip the page budget ([#45](https://github.com/anatolykoptev/go-product-search/issues/45)) ([d4c5eee](https://github.com/anatolykoptev/go-product-search/commit/d4c5eeed8f5f32b18ea1559ab88fc9fd0ef13d35))

## [1.6.0](https://github.com/anatolykoptev/go-product-search/compare/v1.5.0...v1.6.0) (2026-09-26)


### Added

* **extract:** charge browser budget per resolution ([#43](https://github.com/anatolykoptev/go-product-search/issues/43)) ([e279535](https://github.com/anatolykoptev/go-product-search/commit/e27953547ec042baca52006a44175570f87ecdeb))

## [1.5.0](https://github.com/anatolykoptev/go-product-search/compare/v1.4.0...v1.5.0) (2026-09-26)


### Added

* **extract:** dedicated browser-session budget for outbound resolution ([#41](https://github.com/anatolykoptev/go-product-search/issues/41)) ([da649e1](https://github.com/anatolykoptev/go-product-search/commit/da649e1d0b71e97c893eb1e082b276445a64e810))

## [1.4.0](https://github.com/anatolykoptev/go-product-search/compare/v1.3.3...v1.4.0) (2026-09-25)


### Added

* **extract:** surface silent outbound-hop exits ([#38](https://github.com/anatolykoptev/go-product-search/issues/38)) ([7527522](https://github.com/anatolykoptev/go-product-search/commit/7527522255871ac22a5e7598a1782afc6c6439e0))

## [1.3.3](https://github.com/anatolykoptev/go-product-search/compare/v1.3.2...v1.3.3) (2026-09-25)


### Changed

* **extract:** extract interact-yield warn helper ([#36](https://github.com/anatolykoptev/go-product-search/issues/36)) ([d79a8cc](https://github.com/anatolykoptev/go-product-search/commit/d79a8cc461ce802244a674481a902fe995b03ce1))

## [1.3.2](https://github.com/anatolykoptev/go-product-search/compare/v1.3.1...v1.3.2) (2026-09-25)


### Fixed

* **extract:** navigate outbound hop via request URL, not in-session action ([#33](https://github.com/anatolykoptev/go-product-search/issues/33)) ([e4062ec](https://github.com/anatolykoptev/go-product-search/commit/e4062ecbb19799277118d3b1ec33927ce4fd18a0))

## [1.3.1](https://github.com/anatolykoptev/go-product-search/compare/v1.3.0...v1.3.1) (2026-09-25)


### Fixed

* **extract:** decode string-wrapped interact payloads ([#31](https://github.com/anatolykoptev/go-product-search/issues/31)) ([02ddd7a](https://github.com/anatolykoptev/go-product-search/commit/02ddd7a6f6b589518b6231b06b7da8eb4cef21dd))

## [1.3.0](https://github.com/anatolykoptev/go-product-search/compare/v1.2.0...v1.3.0) (2026-09-25)


### Added

* **extract:** resolve outbound merchant links for deal-aggregator cards ([#29](https://github.com/anatolykoptev/go-product-search/issues/29)) ([a7db03b](https://github.com/anatolykoptev/go-product-search/commit/a7db03b59a34f1b46a2a51d0f02b2a95db39cc58))

## [1.2.0](https://github.com/anatolykoptev/go-product-search/compare/v1.1.0...v1.2.0) (2026-09-25)


### Added

* **extract:** follow deal trackers to merchant pages in interact tier ([#27](https://github.com/anatolykoptev/go-product-search/issues/27)) ([b30b42a](https://github.com/anatolykoptev/go-product-search/commit/b30b42a9396e7e578d1329f513e471d2a1d8aeb9))

## [1.1.0](https://github.com/anatolykoptev/go-product-search/compare/v1.0.4...v1.1.0) (2026-09-25)


### Added

* **extract:** interact solve tier for CF-walled detail pages ([#25](https://github.com/anatolykoptev/go-product-search/issues/25)) ([4a51c54](https://github.com/anatolykoptev/go-product-search/commit/4a51c543ab8e3d26ae7f171ee1cd74fed5b7f498))

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
