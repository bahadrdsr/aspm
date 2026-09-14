# M00 operator assumptions and bounded workflow comparison

Date: September 14, 2026. Status: desk research and design decisions, pending
operator trials and independent review. No competitor installation, customer
interview, benchmark or live connector verification was performed by this coder.

## Three workflows, not a connector-count contest

The following primary documentation was inspected for this increment. Mutable
documentation is a dated desk-review snapshot, not evidence of an exact deployed
version or a hands-on usability comparison.

| Workflow | Baseline and evidence | aspm decision |
| --- | --- | --- |
| Import again, preserve triage and explain a change | DefectDojo's current reimport documentation distinguishes matching, creation, closure, reopening and import history; it also describes tool-specific stable identifiers and a Pro location-drift option [R1]. The 2.53 release notes document batched deduplication [R2]. | Reuse established scanner outputs, but implement our own bounded observation/receipt and source-time contracts. Preserve per-scan provenance and core-owned human decisions. Do not claim competitors lack history or matching. |
| Hand off an existing specialist's findings | Dependency-Track documents a DefectDojo integration with per-project mapping and reimport options [R3]. Dependency-Track 5.0 was announced on June 9, 2026, with a redesigned processing architecture [R4]. | Consume specialist outputs instead of rebuilding SBOM/SCA engines. A future Dependency-Track adapter is an expansion candidate, not a ninth mandatory launch family or a replacement for the eight confirmed families. Validate version/field semantics before adoption. |
| Repeated upload and useful bounded export | Faraday's published 5.14.1 notes, dated June 4, 2025, identify duplicate-upload and CSV-export-memory fixes [R5]. These are historical fixes, not a claim of current defects. | Include duplicate-intake receipts, permission-consistent saved filters and asynchronous bounded exports in our acceptance work. Do not clone Faraday's full pentest platform or assert a current performance advantage. |

Primary references:

```text
[R1] https://docs.defectdojo.com/import_data/import_intro/reimport/
[R2] https://docs.defectdojo.com/releases/os_upgrading/2.53/
[R3] https://docs.dependencytrack.org/integrations/defectdojo/
[R4] https://dependencytrack.org/news/dependency-track-5-0/
[R5] https://raw.githubusercontent.com/infobyte/faraday/master/RELEASE.md
```

This is sufficient to choose bounded intake, explicit lifecycle semantics and
output integration rather than a new scanning engine. It is not sufficient to
approve a polished UI, platform capacity, source entitlement or customer fit.
Before broad feature investment, operators must perform the same synthetic
three-workflow trial in selected exact OSS releases and the aspm prototype.
Record actions, typing, elapsed effort, scope surprises and permission failures.

## Traceability to the approved plan's evidence

CE identifiers below retain the September 14, 2026 plan's classification.
They are not new independent observations made by this coder:

| Evidence | Plan's scope and limitation retained here |
| --- | --- |
| CE01 | ArmorCode 2024-2025 review excerpts: configuration/navigation/sync friction, not a representative survey |
| CE02-CE03 | Nucleus 2025-2026 individual passages: mixed reporting/configuration feedback; some invitation/incentive labels |
| CE04 | Apiiro review excerpts with inconsistent individual date visibility: useful inventory context plus reported tuning/handoff friction |
| CE05-CE06 | OX review on March 20, 2025 and a retest request on November 13, 2024; not proof of a current missing feature |
| CE07-CE09 | Aikido reviews on February 3, May 4 and January 27, 2026; grouping/advanced controls, one agent-memory report and portable handoff request, not platform-wide benchmarks |
| CE10 | DefectDojo documentation with OSS/Pro distinctions; rechecked as R1 above |
| CE11 | DefectDojo 2.51.2 history-growth issue opened November 25, 2025; an operator report, not a verified diagnosis of a current version |
| CE12-CE14 | Faraday README and historical 5.14.1 / plugins 1.27.1 fixes; Faraday 5.14.1 notes rechecked as R5 |
| CE15-CE16 | Dependency-Track reports on April 22, 2026 and December 17, 2025; causation unconfirmed and not automatically applicable to v5 |
| CE17-CE18 | Dependency-Track v5 announcement and integration documentation, rechecked as R3/R4 |

No review ranking, inaccessible full review, AI-generated summary or integration
count is used as evidence of accuracy. The underlying plan retains its public
source links and date/edition distinctions. Those reports inspire our gates;
they do not prove our design is better or that another tool still has a bug.

## Operator regression map

| Scenario | Evidence origin | M00 contract response | Later executable gate |
| --- | --- | --- | --- |
| OF-01 permission/coverage gap | CE01, CE05 | Per-profile read permissions, separate health dimensions, conditional prerequisite branch | M03/M07/M09 source setup and actual partial authorization |
| OF-02 source semantic fidelity | CE14 | Original high severity and raw digest retained alongside derived medium; impact/remediation/unmapped fields | M05/M07 parser-version fixture corpus |
| OF-03 stable identity/location | CE10, CE13 | Scan/variant/instance identities, core-owned notes and reversible proposals | M05/M06 actual location-change and multi-source reconciliation |
| OF-04 polling versus new scans/history | CE11, CE17 | Repeat-import/new-scan fixtures, churn/growth/retention hypotheses | M05/M06 ledger, archive and restore tests |
| OF-05 background stall | CE16 | Separate pools and 15-second visible-stall hypothesis | M02/M09 controlled dependency failure with responsive API |
| OF-06 filter/export consistency | CE02, CE03, CE07 | One authorized filter/snapshot identity; bounded UI rendering | M05/M10 equivalent UI/API/export membership and permission revocation |
| OF-07 developer handoff | CE01, CE04, CE07 | Compact owner/freshness/evidence/next-action storyboard and action budgets | M04-M08 rendered workflow and context restoration |
| OF-08 missing ticket fields/acknowledgment | CE01, CE04, CE06 | Jira required-field mapping, preview, uncertain-delivery and idempotent-retry plan | M08 real controlled ticket/receipt behavior |
| OF-09 portable remediation context | CE09 | Explicit copy feedback, preserved access/verification, no AI prerequisite | M08/M11 export/handoff without model execution |
| OF-10 upgrade/parser-change semantics | CE13, CE14, CE17 | Renormalization fixture, immutable original evidence, preview and restore bounds | M06/M07/M13 migration of decisions, templates and saved views |

UX-01/02 map to the three-stage wizard and six advanced branches; UX-03/04 to
the seven action budgets; UX-05/06 to honest catalog support/connection states;
UX-07/08 to reduced motion, bounded rendering and measured feedback; UX-09/10
to the distinct-state storyboard and independent rendered review. The test
author's mapping is authoritative and was not rewritten by the coder.

## Pilot choices and assumptions

Start with **SARIF 2.1.0** for report intake because the first trial needs a
portable structured source with locations and evidence. Native pilot choices:
GitHub.com App/REST 2026-03-10; GitLab.com REST v4 job artifacts; Azure DevOps
Services REST 7.1 build artifacts; scoped AWS EC2/Security Hub CSPM ASFF;
Azure public Resource Graph 2022-10-01 and assessments 2021-06-01; Jira Cloud
REST v3; Teams Workflows standard-channel delivery; Slack workspace bot.
Exact auth/capability/unsupported boundaries are in the integration catalog.

These candidates minimize different operator tasks to validate: repository
ownership, existing pipeline reports, cloud visibility and developer handoff.
They are **not choices endorsed by interviewed customers**. SaaS editions,
entitlements, channel policies, source versions and actual credentials remain
to be confirmed in an authorized pilot before support is advertised.

Unresolved validation questions:

- Can operators complete the common install without tuning, source accounts
  or help, and understand only the prerequisite branch that appears?
- Does the compact queue expose enough ownership/evidence while hiding expert
  records, without making necessary context inaccessible?
- Are the three selected comparison workflows the highest-value daily tasks
  for both application developers and security operators?
- Do 90/180/365/730-day hot/raw/archive/audit policies meet their obligations,
  costs and holds? These are adjustable hypotheses, not legal retention advice.
- Can a standard-channel Teams Workflow have durable ownership and acceptable
  trigger authentication under the pilot tenant's policy?
- What exact operator-owned source/model versions, data approval and resource
  limits are available? Missing access remains unverified, not a passed gate.

Record operator consent and use synthetic fixtures in those trials. Keep
competitor credentials, customer data and screenshots out of the repository.
