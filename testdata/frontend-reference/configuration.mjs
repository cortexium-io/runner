import {mkdir,readFile,writeFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

// Synthetic project configuration references canonical example definitions;
// fixture execution never launches their model/release/product capabilities.
export async function referenceFixture(root,{binary,policy}) {
  const examples=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../../examples/jacks-break');
  const processFile=path.join(examples,'planned-frontend.json'),process=JSON.parse(await readFile(processFile));
  const roles={};
  const visit=steps=>{for(const step of steps)if(step.for_each)visit(step.for_each.steps);else roles[step.role]={file:path.join(examples,'roles',step.role+'.json')};};
  visit(process.steps);
  const focused=JSON.parse(await readFile(roles['frontend-check'].file));
  await mkdir(path.join(root,'shared-go-cache'));
  focused.execution.command.env={PATH:'/usr/bin:/bin',OPENSSL_CONF:'/dev/null',RUNNER_NODE_MODULES:path.join(root,'node-modules'),RUNNER_GO_MODULES:path.join(root,'go-modules'),GOCACHE:path.join(root,'shared-go-cache')};
  const focusedFile=path.join(root,'focused-role.json');await writeFile(focusedFile,JSON.stringify(focused));roles['frontend-check']={file:focusedFile};
  await mkdir(path.join(root,'workspace'),{recursive:true});
  const project={id:'jacks-break',name:'Synthetic frontend intake',workspace:path.join(root,'workspace'),source:{ref:'1'.repeat(40)},default_process:'deliver-planned-frontend',processes:{'deliver-planned-frontend':processFile},roles};
  const instance={projects:['project.json'],concurrency:1,max_active_attempts:2,task_concurrency:2,enforce_work_limits:false,reconcile_interval:'5m',storage:policy,
    codex:{binary,storage_limit:'16MiB',go_binary:binary,go_check_binary:binary},release:{command:[binary],github:binary}};
  for(const [name,value] of [['project.json',project],['instance.json',instance]])await writeFile(path.join(root,name),JSON.stringify(value),{mode:0o600});
  return path.join(root,'instance.json');
}
