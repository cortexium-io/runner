# Campaign Studio qualification contract

This is a small working SaaS qualification, not a full Scratcher clone or a
production-readiness claim. The original goal remains authoritative through
planning, coding, correction and independent verification.

Use the selected installed Genny CLI to create an ordinary Go/Vue app with
password authentication, tenancy and multi-user tenant membership. Use real
sessions; do not enable fake user headers or development auth bypasses.

The fixture has tenant A admin, tenant A member and tenant B admin accounts.
Members can create and edit their tenant's draft campaigns. Only tenant admins
can publish campaigns in their selected tenant. Tenant and role come from the
authenticated session. A draft is private; a published campaign has a public
preview. Publication here changes local product state and does not deploy or send
anything externally.

The fixed API is:

- POST /api/auth/login with email/password/tenant_id; session cookie follows.
- GET /api/auth/me identifies the authenticated user and selected tenant.
- GET and POST /api/campaigns; the collection response is a JSON array.
- GET, PUT, PATCH and DELETE /api/campaigns/{id}, following Genny CRUD conventions.
- POST /api/campaigns/{id}/publish is the publication action.
- GET /api/public/campaigns/{id} returns published id, name, description and
  status only. It returns 404 for drafts and unknown IDs, without private fields.

Campaign has id, name, description, status and tenant_id; status is draft or
published. Creation defaults to draft. Generic CRUD must not bypass the publication
action, including by sending status:published on POST, PUT or PATCH. A rejected
mutation or a safely ignored forbidden field is acceptable if read-back proves
the protected state did not change. Foreign IDs must be denied on read, edit,
delete and publish. Tenant-field spoofing must never reassign a record or make
one appear in another tenant. Unauthenticated CRUD is denied.

The fixed UI is:

- Visiting /campaigns without a session shows the real accessible login form.
  Email and Password are labelled controls; Login or Sign in submits it.
- /campaigns has a Campaigns heading, a visible selected-tenant indication and
  a New campaign button or link.
- New campaign opens a form with labelled Name and Description controls and a
  Save draft button. Saving creates a real campaign.
- The list has a link whose accessible name is the campaign name. Opening it
  allows editing Name and Description, with a Save changes button.
- Member editing a draft has no enabled Publish button. An admin can use its
  Publish button, and the UI visibly reports Published on success.
- /preview/{id} shows published name as a heading and the description without
  requiring login. Draft and missing previews show an unavailable state and no
  private campaign content.
- Tenant B cannot see tenant A's campaigns or content through the UI.
- The campaign editor and public preview are usable at desktop and 390px mobile
  width without horizontal overflow or unhandled browser errors.

The initializer owns all paths while scaffolding. Backend and frontend work must
then actually run concurrently in disjoint internal/ and frontend/ paths. A
subsequent integrator builds one final .qualification/server using the app's
required Go toolchain. The root lead independently admits saas-fixed-check as a
dependent of implementation; it runs after the planner settles. The lead then
assesses its actual report against
the goal. No external chat writes app source or performs product qualification
outside Runner.

For this full delivery qualification, a failed product/build result permits at
most two autonomous repair/recheck pairs against the same source and goal. Reuse
passing evidence whose inputs did not change. Do not repeat a completed
initializer, introduce another planning/review ceremony or request more allowance.
Success requires the newest fixed report, the newest build's exact binary hash
and completed browser/server/scratch cleanup; an execution-completed child alone
does not satisfy acceptance.

Run the protected qualify-saas.mjs through the configured fixed command role
without weakening it. It owns one temporary
SQLite database and browser, seeds synthetic users through Genny, exercises the
real HTTP and UI behaviour, and closes/removes its resources in finally. Keep
the report and bounded screenshots in the command's owned evidence archive.
Rerun after a repair only when relevant app/build inputs changed. Keep failed
evidence rather than replacing it with a success claim.

SQLite scoping and trigger-backed writes are the tested database behaviour.
PostgreSQL, SMTP, billing, deployment, high-load operation and broad adversarial
security testing are outside this qualification. Report these limits plainly.
