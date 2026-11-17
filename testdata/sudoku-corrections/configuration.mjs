// Synthetic accepted-parent/configuration data. No product source or worker.
export function parentFixture(root,binary) {
  const execution=access=>({harness:'native',access,model:'gpt-6.2-sol',reasoning:'high',max_input:65536,max_output:8192,instructions:'Historical accepted instructions.',
    native:{harness:'codex',binary,context:'lean',tools:{mode:'native'},skills:{mode:'none'},runtime_write_paths:[root+'/cache']}});
  const parent={id:'run_accepted',revision:7,status:'Completed',resources_released:true,project_id:'sudoku-preview',repository:root+'/source',
    task_graph:{root_run_id:'run_accepted'},attempts:[{id:'attempt_lead',state:'finished',released:true}],
    inputs:{patch:{artifact_id:'attempt_patch/changes.patch'},checks:{artifact_id:'attempt_frontend/result'}},roles:{},
    process:{id:'followup-sudoku-preview',max_attempts:128,max_attempts_per_step:4,infrastructure_retries:1,attempt_timeout:'20m',
      inputs:{request:'text'},tasks:{roles:['sudoku-developer'],max_tasks:64,max_depth:2,max_revisions:0,max_concurrent:2},
      steps:[{id:'lead',role:'sudoku-lead',inputs:{request:{from_input:'request'}}}],outputs:{result:{from_step:'lead',output:'report'}}}};
  for(const [kind,id] of [['lead','sudoku-lead'],['developer','sudoku-developer'],['source','sudoku-source-review'],['visual','sudoku-ux-review']]) {
    const profile=execution(kind==='developer'?'workspace-write':'workspace-read');
    if(kind==='lead')profile.reasoning='medium';
    parent.roles[id]={id,purpose:'Historical '+kind,inputs:{},outputs:{report:'text'},execution:profile};
  }
  parent.roles['sudoku-check']={id:'sudoku-check',purpose:'Historical fixed checker',inputs:{checks:'artifact'},outputs:{check:'text',artifact:'artifact'},execution:{
    harness:'command',mode:'shared',access:'workspace-read',command:{
      args:[binary,'--node',binary,'--go',binary,'--modules',root+'/modules','--playwright',root+'/playwright/index.mjs','--chromium',binary,'--webkit',binary,'--preview-root',root+'/old-preview'],
      env:{PATH:'/usr/bin:/bin',GOCACHE:root+'/cache',GOMODCACHE:root+'/modules-cache',OPENSSL_CONF:'/dev/null',RUNNER_SUDOKU_FRONTEND_HELPER:binary},
      runtime_write_paths:[root+'/cache',root+'/old-preview'],network:true,result:'domain.json',artifact:'evidence.tar.gz'
    }}};
  return parent;
}

export function goalFixture(parent) {
  return {request_id:'owner-corrections-fixture',workspace:parent.repository,followup:{run_id:parent.id,revision:parent.revision},
    accepted_candidate:{source_sha256:'a'.repeat(64),check_sha256:'b'.repeat(64),check_artifact:'attempt_accepted_check/result',handoff_artifact:'attempt_handoff/result'},
    corrections:Array.from({length:14},(_,i)=>({id:'C'+String(i+1).padStart(2,'0'),requirement:'Observable owner correction '+(i+1)})),
    phone_preview:{required:true},delivery_stop:'Owner inspection; no release'};
}
