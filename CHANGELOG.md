# Changelog

## [1.23.2](https://github.com/anatolykoptev/quarryn/compare/v1.23.1...v1.23.2) (2026-09-28)


### Fixed

* **extract:** clip over-cap optional fields, don't fail extraction ([#122](https://github.com/anatolykoptev/quarryn/issues/122)) ([5a79711](https://github.com/anatolykoptev/quarryn/commit/5a79711a06684a01293145a1500d1cedca5f47b1))

## [1.23.1](https://github.com/anatolykoptev/quarryn/compare/v1.23.0...v1.23.1) (2026-09-28)


### Fixed

* **extract:** .js rescue reaches walled shopify pages ([#119](https://github.com/anatolykoptev/quarryn/issues/119)) ([760ab6b](https://github.com/anatolykoptev/quarryn/commit/760ab6bad70a01a21dfc1ad558424c4fe97e864e))

## [1.23.0](https://github.com/anatolykoptev/quarryn/compare/v1.22.0...v1.23.0) (2026-09-28)


### Added

* variant matrix end-to-end — judge context, results, watch pinning ([#115](https://github.com/anatolykoptev/quarryn/issues/115)) ([#116](https://github.com/anatolykoptev/quarryn/issues/116)) ([9e5ae4e](https://github.com/anatolykoptev/quarryn/commit/9e5ae4e035aa65c225dc6a40eb22078e6c29f029))

## [1.22.0](https://github.com/anatolykoptev/quarryn/compare/v1.21.0...v1.22.0) (2026-09-28)


### Added

* **api:** url field carries the purchase page, source_url the listing ([#113](https://github.com/anatolykoptev/quarryn/issues/113)) ([bb1dc11](https://github.com/anatolykoptev/quarryn/commit/bb1dc11a29fa3a6904fadd0745af3e625b0fd4f4))

## [1.21.0](https://github.com/anatolykoptev/quarryn/compare/v1.20.0...v1.21.0) (2026-09-28)


### Added

* **watch:** webhook HMAC-V2 signing + chat_id (Hermes bot lane) ([#109](https://github.com/anatolykoptev/quarryn/issues/109)) ([31ed82a](https://github.com/anatolykoptev/quarryn/commit/31ed82a090e922ff56d36843999506e2c1889382))

## [1.20.0](https://github.com/anatolykoptev/quarryn/compare/v1.19.0...v1.20.0) (2026-09-28)


### Added

* **watch,orders:** tenant owner scoping + tg-routed alerts ([#106](https://github.com/anatolykoptev/quarryn/issues/106)) ([b3eef40](https://github.com/anatolykoptev/quarryn/commit/b3eef40a1cd93b4e33ffeef4da565a28cdbdfd1f))

## [1.19.0](https://github.com/anatolykoptev/quarryn/compare/v1.18.1...v1.19.0) (2026-09-27)


### Added

* **watch:** generic JSON webhook notifier (WATCH_NOTIFY_FORMAT=json) ([#104](https://github.com/anatolykoptev/quarryn/issues/104)) ([3c46f64](https://github.com/anatolykoptev/quarryn/commit/3c46f648c053991a2b093d657746830d5748d11a))

## [1.18.1](https://github.com/anatolykoptev/quarryn/compare/v1.18.0...v1.18.1) (2026-09-27)


### Fixed

* **build:** release-please-bumped version fallback for dozor worktree builds ([#102](https://github.com/anatolykoptev/quarryn/issues/102)) ([130c355](https://github.com/anatolykoptev/quarryn/commit/130c355ab54b8c977269767b84fee10ea3d2910c))

## [1.18.0](https://github.com/anatolykoptev/quarryn/compare/v1.17.1...v1.18.0) (2026-09-27)


### Added

* **watch:** restock, percent-drop and condition-gated triggers + history ([#101](https://github.com/anatolykoptev/quarryn/issues/101)) ([58a0c6c](https://github.com/anatolykoptev/quarryn/commit/58a0c6cf78525563fafcf02206da19e62e7cedb4))


### Documentation

* reader-grade README — live example up front, no filler ([#92](https://github.com/anatolykoptev/quarryn/issues/92)) ([40a0ce5](https://github.com/anatolykoptev/quarryn/commit/40a0ce5299ca44ef6d3650a6a567c899c437a46a))
* rewrite README for public readers, move env table to OPERATIONS ([#90](https://github.com/anatolykoptev/quarryn/issues/90)) ([f90fd71](https://github.com/anatolykoptev/quarryn/commit/f90fd71b251116059ac6c0310701895cf69980fd))

## [1.17.1](https://github.com/anatolykoptev/quarryn/compare/v1.17.0...v1.17.1) (2026-09-27)


### Changed

* rename go-product-search → quarryn and scrub for public release ([#89](https://github.com/anatolykoptev/quarryn/issues/89)) ([f0f58cf](https://github.com/anatolykoptev/quarryn/commit/f0f58cfd5cf9e6421524952d79e8bd14f36d3d2a))


### Documentation

* add Apache-2.0 license ([8230a1e](https://github.com/anatolykoptev/quarryn/commit/8230a1e9bbfa6dd8b2cc87da4c30c071e75ec1bf))
* add PRODUCT.md — vision, pillars, positioning, monetization ([3c96dc0](https://github.com/anatolykoptev/quarryn/commit/3c96dc028f73401896a484bfbac2b6d8dff6b82e))
* release documentation pack ([#88](https://github.com/anatolykoptev/quarryn/issues/88)) ([14fc71b](https://github.com/anatolykoptev/quarryn/commit/14fc71bd984d9d7b9b7583a4d8766e387ddf67dd))

## [1.17.0](https://github.com/anatolykoptev/quarryn/compare/v1.16.1...v1.17.0) (2026-09-27)


### Added

* order tracking via confirmation-email parsing ([#57](https://github.com/anatolykoptev/quarryn/issues/57)) ([a76d85e](https://github.com/anatolykoptev/quarryn/commit/a76d85ed39c3144406f47046861ec20a38c530eb))

## [1.16.1](https://github.com/anatolykoptev/quarryn/compare/v1.16.0...v1.16.1) (2026-09-27)


### Fixed

* product_watch gets search-tier timeout ([0a33aaa](https://github.com/anatolykoptev/quarryn/commit/0a33aaa5b97cec02aabd25493f3eda2eb90b5939))
* watch cancel accepts any non-terminal status ([c830b09](https://github.com/anatolykoptev/quarryn/commit/c830b099fca13669d4bbca4b8221c43492b7c5f1))

## [1.16.0](https://github.com/anatolykoptev/quarryn/compare/v1.15.0...v1.16.0) (2026-09-27)


### Added

* price watches on PG18 — offer/query watches, at-least-once notify ledger ([#53](https://github.com/anatolykoptev/quarryn/issues/53)) ([6db6e25](https://github.com/anatolykoptev/quarryn/commit/6db6e25c9a2a36d7ec9221348efa94ba6990de29))

## [1.15.0](https://github.com/anatolykoptev/quarryn/compare/v1.14.0...v1.15.0) (2026-09-27)


### Added

* adapter manifest — allowedHosts + userSession, funnel-enforced ([#78](https://github.com/anatolykoptev/quarryn/issues/78)) ([5e3ee6c](https://github.com/anatolykoptev/quarryn/commit/5e3ee6c76b79cf5bff210e494093797fb0d46bbc))

## [1.14.0](https://github.com/anatolykoptev/quarryn/compare/v1.13.0...v1.14.0) (2026-09-27)


### Added

* Postgres feedback sink on the fleet-shared instance ([#77](https://github.com/anatolykoptev/quarryn/issues/77)) ([9f99b47](https://github.com/anatolykoptev/quarryn/commit/9f99b47a3f169140c3d6d66c8548f7fdd224983b))
* stable offer-ID codec for same-listing re-resolution ([#75](https://github.com/anatolykoptev/quarryn/issues/75)) ([0b283ac](https://github.com/anatolykoptev/quarryn/commit/0b283ac39f22e51ca84dd6dde95b2ba9608815c4))

## [1.13.0](https://github.com/anatolykoptev/quarryn/compare/v1.12.0...v1.13.0) (2026-09-27)


### Added

* condition enum end-to-end — adapter marks + deterministic condition:ENUM ([#73](https://github.com/anatolykoptev/quarryn/issues/73)) ([a86eaab](https://github.com/anatolykoptev/quarryn/commit/a86eaab4da787765658e3f5cade6acfa79830fc2))

## [1.12.0](https://github.com/anatolykoptev/quarryn/compare/v1.11.1...v1.12.0) (2026-09-27)


### Added

* **trust:** seed trust provider ([2e60b64](https://github.com/anatolykoptev/quarryn/commit/2e60b644a0e6bfcd16758816cfccbcebd48c25c5)), closes [#51](https://github.com/anatolykoptev/quarryn/issues/51)

## [1.11.1](https://github.com/anatolykoptev/quarryn/compare/v1.11.0...v1.11.1) (2026-09-26)


### Fixed

* **sources:** cover gad_*/srsltid/ad-network click ids ([a9f2611](https://github.com/anatolykoptev/quarryn/commit/a9f2611f6c68019ebff4a93d8268aff5323899f6)), closes [#68](https://github.com/anatolykoptev/quarryn/issues/68)

## [1.11.0](https://github.com/anatolykoptev/quarryn/compare/v1.10.0...v1.11.0) (2026-09-26)


### Added

* **api:** code-composed search brief ([7ba0afe](https://github.com/anatolykoptev/quarryn/commit/7ba0afe35913504e055804b33506789af2611ed4)), closes [#54](https://github.com/anatolykoptev/quarryn/issues/54)

## [1.10.0](https://github.com/anatolykoptev/quarryn/compare/v1.9.0...v1.10.0) (2026-09-26)


### Added

* **extract:** minor-unit money representation ([85ae0ee](https://github.com/anatolykoptev/quarryn/commit/85ae0ee76905261825f93271ab5239b6da0982a9)), closes [#49](https://github.com/anatolykoptev/quarryn/issues/49)

## [1.9.0](https://github.com/anatolykoptev/quarryn/compare/v1.8.0...v1.9.0) (2026-09-26)


### Added

* **match:** typed ReasonCode + exclude_detail on candidates ([e7c429d](https://github.com/anatolykoptev/quarryn/commit/e7c429dc3c94d89ee0db5e3fd797dc9ba12941ff)), closes [#48](https://github.com/anatolykoptev/quarryn/issues/48)

## [1.8.0](https://github.com/anatolykoptev/quarryn/compare/v1.7.0...v1.8.0) (2026-09-26)


### Added

* **sources:** strip affiliate/tracker params off candidate URLs at the funnel boundary ([#60](https://github.com/anatolykoptev/quarryn/issues/60)) ([3c373ed](https://github.com/anatolykoptev/quarryn/commit/3c373ede1a4a9dce78b562b630add9b548425f1e))

## [1.7.0](https://github.com/anatolykoptev/quarryn/compare/v1.6.1...v1.7.0) (2026-09-26)


### Added

* **sources:** Shopify UCP catalog legs — per-shop search_catalog + global catalog ([9f85807](https://github.com/anatolykoptev/quarryn/commit/9f85807d46afccd57ec4cac6256dc27c4754db20))

## [1.6.1](https://github.com/anatolykoptev/quarryn/compare/v1.6.0...v1.6.1) (2026-09-26)


### Fixed

* **sources:** interact calls keep pacing but skip the page budget ([#45](https://github.com/anatolykoptev/quarryn/issues/45)) ([d4c5eee](https://github.com/anatolykoptev/quarryn/commit/d4c5eeed8f5f32b18ea1559ab88fc9fd0ef13d35))

## [1.6.0](https://github.com/anatolykoptev/quarryn/compare/v1.5.0...v1.6.0) (2026-09-26)


### Added

* **extract:** charge browser budget per resolution ([#43](https://github.com/anatolykoptev/quarryn/issues/43)) ([e279535](https://github.com/anatolykoptev/quarryn/commit/e27953547ec042baca52006a44175570f87ecdeb))

## [1.5.0](https://github.com/anatolykoptev/quarryn/compare/v1.4.0...v1.5.0) (2026-09-26)


### Added

* **extract:** dedicated browser-session budget for outbound resolution ([#41](https://github.com/anatolykoptev/quarryn/issues/41)) ([da649e1](https://github.com/anatolykoptev/quarryn/commit/da649e1d0b71e97c893eb1e082b276445a64e810))

## [1.4.0](https://github.com/anatolykoptev/quarryn/compare/v1.3.3...v1.4.0) (2026-09-25)


### Added

* **extract:** surface silent outbound-hop exits ([#38](https://github.com/anatolykoptev/quarryn/issues/38)) ([7527522](https://github.com/anatolykoptev/quarryn/commit/7527522255871ac22a5e7598a1782afc6c6439e0))

## [1.3.3](https://github.com/anatolykoptev/quarryn/compare/v1.3.2...v1.3.3) (2026-09-25)


### Changed

* **extract:** extract interact-yield warn helper ([#36](https://github.com/anatolykoptev/quarryn/issues/36)) ([d79a8cc](https://github.com/anatolykoptev/quarryn/commit/d79a8cc461ce802244a674481a902fe995b03ce1))

## [1.3.2](https://github.com/anatolykoptev/quarryn/compare/v1.3.1...v1.3.2) (2026-09-25)


### Fixed

* **extract:** navigate outbound hop via request URL, not in-session action ([#33](https://github.com/anatolykoptev/quarryn/issues/33)) ([e4062ec](https://github.com/anatolykoptev/quarryn/commit/e4062ecbb19799277118d3b1ec33927ce4fd18a0))

## [1.3.1](https://github.com/anatolykoptev/quarryn/compare/v1.3.0...v1.3.1) (2026-09-25)


### Fixed

* **extract:** decode string-wrapped interact payloads ([#31](https://github.com/anatolykoptev/quarryn/issues/31)) ([02ddd7a](https://github.com/anatolykoptev/quarryn/commit/02ddd7a6f6b589518b6231b06b7da8eb4cef21dd))

## [1.3.0](https://github.com/anatolykoptev/quarryn/compare/v1.2.0...v1.3.0) (2026-09-25)


### Added

* **extract:** resolve outbound merchant links for deal-aggregator cards ([#29](https://github.com/anatolykoptev/quarryn/issues/29)) ([a7db03b](https://github.com/anatolykoptev/quarryn/commit/a7db03b59a34f1b46a2a51d0f02b2a95db39cc58))

## [1.2.0](https://github.com/anatolykoptev/quarryn/compare/v1.1.0...v1.2.0) (2026-09-25)


### Added

* **extract:** follow deal trackers to merchant pages in interact tier ([#27](https://github.com/anatolykoptev/quarryn/issues/27)) ([b30b42a](https://github.com/anatolykoptev/quarryn/commit/b30b42a9396e7e578d1329f513e471d2a1d8aeb9))

## [1.1.0](https://github.com/anatolykoptev/quarryn/compare/v1.0.4...v1.1.0) (2026-09-25)


### Added

* **extract:** interact solve tier for CF-walled detail pages ([#25](https://github.com/anatolykoptev/quarryn/issues/25)) ([4a51c54](https://github.com/anatolykoptev/quarryn/commit/4a51c543ab8e3d26ae7f171ee1cd74fed5b7f498))

## [1.0.4](https://github.com/anatolykoptev/quarryn/compare/v1.0.3...v1.0.4) (2026-09-25)


### Fixed

* **sources:** surface slickdeals card data, AND-match shopify queries ([#23](https://github.com/anatolykoptev/quarryn/issues/23)) ([8b56387](https://github.com/anatolykoptev/quarryn/commit/8b563877a80239787e68aac3bb4bab60ad5709b6))

## [1.0.3](https://github.com/anatolykoptev/quarryn/compare/v1.0.2...v1.0.3) (2026-09-25)


### Fixed

* **extract:** handle schema.org ProductGroup variant pages ([#21](https://github.com/anatolykoptev/quarryn/issues/21)) ([2c11b6a](https://github.com/anatolykoptev/quarryn/commit/2c11b6ac6bc07cdaa15dd00b36a7a12011da3d13))

## [1.0.2](https://github.com/anatolykoptev/quarryn/compare/v1.0.1...v1.0.2) (2026-09-25)


### Fixed

* **extract:** render tier waits domcontentloaded with doubled timeout ([#18](https://github.com/anatolykoptev/quarryn/issues/18)) ([73efce1](https://github.com/anatolykoptev/quarryn/commit/73efce104e13564fef654772f70afd1ca2adc49b))

## [1.0.1](https://github.com/anatolykoptev/quarryn/compare/v1.0.0...v1.0.1) (2026-09-25)


### Fixed

* **extract:** escalate CF-signature fetch errors to render tier ([#16](https://github.com/anatolykoptev/quarryn/issues/16)) ([651beb2](https://github.com/anatolykoptev/quarryn/commit/651beb2bc52f93d5c106676036295ca055f3951b))

## 1.0.0 (2026-09-25)


### Added

* candidate sourcing stage — marketplace adapters + merge/dedup funnel (p2) ([2747757](https://github.com/anatolykoptev/quarryn/commit/2747757b371db713e6bd1eb3b5fd805752bd4995))
* extraction+normalization stage — schema.org parse, fenced LLM fallback, extraction cache (p3) ([1948d2f](https://github.com/anatolykoptev/quarryn/commit/1948d2f2d10f9752a8ce62630c1ec36b07ae29b4))
* jeff match stage — criteria planner, deterministic prefilter, packed noul Ask, loud degrade (p4) ([867a553](https://github.com/anatolykoptev/quarryn/commit/867a5532650cda770dcec831bfe5a583d453d50c))
* P6 cost + resilience hardening — budgets, pacing, render tier, feedback sink ([5793f6c](https://github.com/anatolykoptev/quarryn/commit/5793f6c79ec2b96f6a9003d0e2101b78de8bcea2))
* P7 calibration ids + acceptance probes — request_id pairing, product_probe, runbook ([f2dd19a](https://github.com/anatolykoptev/quarryn/commit/f2dd19aafbb91d955c5e2165d42e2df7c0038e4c))
* rank fusion + MCP tool surface — product_search/product_match, public projection (p5) ([8cd3d04](https://github.com/anatolykoptev/quarryn/commit/8cd3d04ea776ea441d3c948fc5d000b2cc4e0367))
* scaffold quarryn service (P1) ([72534ea](https://github.com/anatolykoptev/quarryn/commit/72534eac47c964d1764e160244f64bb06cecccac))
* service scaffold — MCP+REST server, bearer auth, config, deploy wiring ([e31458e](https://github.com/anatolykoptev/quarryn/commit/e31458e4165bb8550ca0e81e9293231290674a5b))


### Fixed

* **match:** review findings — timeout propagation, UTF-8 boundary, NaN bounds, noul clamp, degrade-metric hygiene ([f9df098](https://github.com/anatolykoptev/quarryn/commit/f9df0980e6dc89f9434cc16696d5064ef68af20f))
* **p6:** review nits — feedback tool not read-only, nil-response guard in fetchgate ([c66d964](https://github.com/anatolykoptev/quarryn/commit/c66d96413e2d5e8fbbdaf2a9dc64d1318d9fe1eb))
