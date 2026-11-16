export const verifierEntry='frontend/verification/project-experience.mjs';
export const projectCheckID='frontend-project-check';
export const projectExperienceID='frontend-project-experience';
export const assemblyID='frontend-assemble-checks';
export const planID='frontend-request-plan';
export const ownerID='frontend-request-owner';

function profiles(role) {return role.execution?[role.execution]:Object.values(role.profiles||{});}
function clonedRole(role,id,instructions,inputs={}) {
  const copy=structuredClone(role);copy.id=id;copy.inputs={...copy.inputs,...inputs};
  for(const profile of profiles(copy))profile.instructions+='\n\n'+instructions;
  return copy;
}

// Project assertions remain future source work inside Runner. This configuration
// supplies execution/feedback/evidence ports and preserves the protected suffix.
export function connectProjectVerification(ordinary,planRole,ownerRole,runtime) {
  const next=structuredClone(ordinary),p=next.process,step=id=>p.steps.find(item=>item.id===id);
  const check=step('check'),measure=step('measure'),source=step('review'),owner=step('owner'),plan=step('plan');
  if(!check||!measure||!source||!owner||!plan||!step('cards'))throw new Error('complete frontend source prefix required');
  const sourceRole=next.roles.find(role=>role.id===source.role),mandatory=next.roles.find(role=>role.id===step('experience-review').role);
  const selected=sourceRole.execution||sourceRole.profiles[runtime.reviewProfile||sourceRole.default_profile];
  if(!selected)throw new Error('selected independent review profile required');
  const baseArgs=[runtime.node,runtime.launcher,'--source','{workspace}','--scratch','{scratch}','--patch','{artifact.patch}','--reference','{artifact.reference}','--entry',verifierEntry];
  const command=(id,purpose,inputs,args,network,env={})=>({id,purpose,inputs,outputs:{check:'text',artifact:'artifact'},execution:{harness:'command',mode:'shared',access:'workspace-read',command:{args,env,network,artifact:'project-evidence.tar.gz',result:'domain.json'}}});
  const projectCheck=command(projectCheckID,'Run the configured candidate-owned verifier with retained source-bound failure diagnostics.',{patch:'artifact',reference:'artifact'},baseArgs,true,{
    PATH:runtime.path,OPENSSL_CONF:runtime.openssl,
    RUNNER_PROJECT_PLAYWRIGHT_MODULE:runtime.playwright,RUNNER_PROJECT_CHROMIUM:runtime.chromium,
    RUNNER_PROJECT_FRONTEND_MODULES:runtime.modules,RUNNER_PROJECT_GO_MODULES:runtime.goModules,RUNNER_PROJECT_GO:runtime.go,
    GOCACHE:runtime.goCache,GOMODCACHE:runtime.goModules
  });
  projectCheck.execution.command.runtime_write_paths=[runtime.goCache];
  const assembly=command(assemblyID,'Assemble original focused proof and current independent project judgment; keep images canonical.',{patch:'artifact',reference:'artifact',focused:'artifact',project:'artifact',experience:'artifact'},
    [...baseArgs,'--assemble','--focused','{artifact.focused}','--project','{artifact.project}','--experience','{artifact.experience}'],false);
  const projectExperience={id:projectExperienceID,purpose:'Independently inspect current project-specific viewport images against the owner goal.',
    inputs:{request:'text',patch:'artifact',reference:'artifact',project:'artifact'},outputs:{report:'artifact',summary:'text'},execution:{
      harness:'native',access:'workspace-read',model:selected.model,reasoning:selected.reasoning,max_input:selected.max_input,max_output:selected.max_output,
      native:{harness:'codex',binary:runtime.nativeBinary,context:'lean',tools:{mode:'native'},skills:{mode:'none'},network:false},
      instructions:[
        'Read the original goal, current patch, owner reference and project archive. This is a separate image-capable native role; the restricted mandatory reviewer remains unchanged. Extract only bounded flat regular JSON/PNG files to owned scratch. Read launcher.json; require status ready, matching current patch SHA and owner request SHA. Personally view EVERY declared project PNG with view_image at readable scale; do not infer visual quality from counts or DOM assertions. Judge current goal, discoverability, operation, hierarchy, geometry, contrast, labels and changed interactions. Label controlled/emulated coverage truthfully; no physical/native Safari claim.',
        'Write project-review.json in output_dir with {verdict:"approved"|"changes_requested"|"blocked",patch_sha256,owner_request_sha256,verifier_sha256,project_artifact_id,project_sha256,inspected_images:[actual names viewed],findings:[],gaps:[]}. Copy identities from current declared inputs and compute the exact archive SHA; never invent them. Missing images, unverifiable identity, unexplained failures or a missing goal requirement cannot approve. Concrete source/verifier repairs use verdict changes_requested; infrastructure/authority gaps use blocked. Return outcome ready when the report is delivered even when it rejects; the fixed assembler checks the verdict. Outputs report:"project-review.json",summary at most160 characters; the WHOLE final JSON must fit max_output_bytes.',
        'Read-only source, supplied artifacts and owned scratch only. No tests, browsers, servers, agents, Git, network, source edits, Runner/config changes, merge or deployment. Clean extraction scratch. Existing focused checks, all nine-family built-image qualification, mandatory independent viewport review, native Safari gate and protected decisions remain required.'
      ].join('\n\n')
    }};
  const planner=clonedRole(planRole,planID,
    'This route has a configured project-owned browser verifier at '+verifierEntry+'. Include a separate bounded test-author card owning only that file and, if necessary, its adjacent frontend/verification/project-experience/ helpers; other product cards must not overlap those paths. The test author creates meaningful request-specific assertions and representative phone/desktop captures from actual current components/app, explicitly controlling external statuses as simulations where required by the owner. Do not author a passing stub or reuse unrelated historical screenshots. The fixed launcher invokes the script as a child Node process with --source/--scratch/--patch/--reference; pinned runtime paths are RUNNER_PROJECT_* environment values. Script writes project-result.json with status (passed, failed or blocked), reason (at most512 UTF-8 bytes), optional summary (at most4096 UTF-8 bytes) and files (flat regular JSON/PNG names); declared files total below16MiB, each below8MiB, max64. Report actual interaction observations, current source/verifier/runtime identities and cleanup. Script owns browser/context/server cleanup in finally; no dependencies, source writes or paid/external ad requests; Go checks reuse the fixed shared GOCACHE. Do not write wrapper-owned domain.json/project-evidence.tar.gz. Runner owns all execution; the author hands off source without running product checks.');
  const repair=clonedRole(ownerRole,ownerID,
    'Read the current checks archive: launcher.json, unchanged focused.json, and project-diagnostic.json/project-review.json when present. Before the first check its fallback is only baseline evidence. Project launcher errors are verifier-code repairs, never permission to weaken checks or change Runner/runtime/gates. Retain current product work and meaningful assertions; repair the relevant project-owned source/verifier coherently using retained failure diagnostics. Configured stages own execution and independent image review; do not invoke those stages externally.');
  plan.role=planID;owner.role=ownerID;
  // Preserve the original focused role, args and routes apart from the forward edge.
  check.id='focused-check';check.routes.ready.next='project-check';
  check.routes.changes_requested.next='check';check.routes.blocked.next='check';
  step('join').routes.ready.next='focused-check';
  measure.routes.ready.next='project-experience';
  const additions=[
    {id:'project-check',role:projectCheckID,inputs:{patch:{from_candidate:true},reference:{from_input:'review_evidence'}},routes:{ready:{next:'measure'},changes_requested:{next:'check'},blocked:{next:'check'}}},
    {id:'project-experience',role:projectExperienceID,inputs:{request:{from_input:'request'},patch:{from_candidate:true},reference:{from_input:'review_evidence'},project:{from_step:'project-check',output:'artifact'}},routes:{ready:{next:'check'},blocked:{next:'check'}}},
    {id:'check',role:assemblyID,inputs:{patch:{from_candidate:true},reference:{from_input:'review_evidence'},focused:{from_step:'focused-check',output:'artifact'},project:{from_step:'project-check',output:'artifact',fallback:{from_step:'focused-check',output:'artifact'}},experience:{from_step:'project-experience',output:'report',fallback:{from_step:'focused-check',output:'artifact'}}},routes:{ready:{next:'review'},changes_requested:{next:'owner',max_times:3},blocked:{next:'$fail'}}}
  ];
  p.steps.splice(p.steps.indexOf(check)+1,0,additions[0]);
  p.steps.splice(p.steps.indexOf(source),0,additions[1],additions[2]);
  for(const profile of profiles(sourceRole))profile.instructions+='\n\nRead assembled checks/launcher.json, unchanged focused.json and project-review.json. Require ready assembled status; exact current patch/owner/verifier/archive identities; approving independent judgment with actual inspected image names and no material gap. Full project images stay canonical in the named project artifact; do not dump/copy them. The independent project review owns request-specific visual judgment; independently verify the source and assertions support it.';
  for(const profile of profiles(mandatory))profile.instructions+='\n\nSupplemental project judgment is in assembled checks: launcher.json and project-review.json, with unchanged focused.json. Require current matching identities, ready assembly and an approving independent project image judgment for the original goal. Personally view every attached mandatory nine-family image. The supplemental images remain canonical in the named project artifact and were viewed by the separate project reviewer; do not claim you viewed them or require them to be attached twice. Missing/rejected/stale supplemental judgment blocks. All existing mandatory sealed viewport/native/release conditions remain required.';
  next.roles.push(planner,repair,projectCheck,projectExperience,assembly);
  return next;
}
