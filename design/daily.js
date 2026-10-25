// Daily navigation retains view context; lifecycle state remains in db.
const projectViews=new Map();let taskReturn=null;
function rememberProject(){if(ui.page!=='workspace'||!ws())return;const file=$('#filename')?.textContent,content=$('#filecontent')?.textContent;const view={workspace:ui.workspace,sandbox:ui.selected[ui.workspace],tab:ui.tab,runTab:ui.runTab,scroll:window.scrollY,file,content};projectViews.set(ui.workspace,view);taskReturn={...view};}
const dailySelectWorkspace=selectWorkspace;
selectWorkspace=function(id){rememberProject();const saved=projectViews.get(id);dailySelectWorkspace(id);if(saved){ui.tab=saved.tab;ui.runTab=saved.runTab;if(db.sandboxes.some(s=>s.id===saved.sandbox&&s.workspace===id))ui.selected[id]=saved.sandbox;render();restoreProjectView(saved);}else window.scrollTo(0,0);};
function restoreProjectView(view){if(view.tab==='files'&&view.file&&$('#filename')){$('#filename').textContent=view.file;$('#filecontent').textContent=view.content;}window.scrollTo(0,view.scroll||0);}
function returnToTask(){if(!taskReturn||!db.workspaces.some(w=>w.id===taskReturn.workspace)){say('原工作区已移除，请从首页选择项目');ui.page='home';render();return}const saved={...taskReturn};selectWorkspace(saved.workspace);ui.tab=saved.tab;ui.runTab=saved.runTab;if(db.sandboxes.some(s=>s.id===saved.sandbox))ui.selected[saved.workspace]=saved.sandbox;render();restoreProjectView(saved);}
// Capture before document handlers, including the state-detail deep link.
window.addEventListener('click',e=>{const b=e.target.closest('button');if(!b||b.disabled)return;
 if(b.dataset.visitRun){e.stopImmediatePropagation();rememberProject();const s=db.sandboxes.find(x=>x.id===b.dataset.visitRun);if(!s||!db.workspaces.some(w=>w.id===s.workspace)){say('该运行的工作区已移除');return}selectWorkspace(s.workspace);ui.selected[s.workspace]=s.id;ui.tab='run';ui.runTab='details';render();return;}
 if(b.dataset.act==='return-task'){e.stopImmediatePropagation();returnToTask();return;}
 if(b.dataset.page||b.dataset.viewState||b.dataset.act==='audit')rememberProject();
 if(b.dataset.page){window.scrollTo(0,0);}
 if(b.dataset.tab||b.dataset.runtab){window.scrollTo(0,0);}
 if(b.dataset.act==='refresh-current'){e.stopImmediatePropagation();ui.tab='run';ui.runTab='terminal';const s=run();if(s?.status==='running'&&s.connection==='detached'){s.connection='connecting';s.reconnecting=true;ready(s);}render();return;}
 if(b.dataset.act==='resume-current'){e.stopImmediatePropagation();launch();return;}
},true);
// One coherent provider card instead of duplicate identity and action lists.
function managementTabs(active){return `<nav class="management-tabs" aria-label="管理"><button data-page="credentials" class="${active==='credentials'?'active':''}">凭证</button><button data-page="mcp" class="${active==='mcp'?'active':''}">MCP</button><button data-page="states" class="${active==='states'?'active':''}">会话状态</button><button data-page="audit" class="${active==='audit'?'active':''}">活动记录</button><button data-page="settings" class="${active==='settings'?'active':''}">设置</button></nav>`;}
credentials=function(){return header('管理','凭证与会话设置')+managementTabs('credentials')+`<div class="content">${['codex','github'].map(p=>`<div class="card"><div class="row"><div><h2>${p==='codex'?'Codex / OpenAI':'GitHub'}</h2><small>${credentialLabels[db.credentials[p]]} · 宿主 Keychain · 示例连接</small></div>${badge(credentialLabels[db.credentials[p]])}</div><p>连接可跨项目使用；不在此页面选择具体沙箱的账号绑定。</p><div class="actions">${button(db.credentials[p]==='connected'?'重新授权':'连接账号','auth-'+p,'primary')}${button('测试连接','test-'+p,'',db.credentials[p]!=='connected')}${p==='codex'?button('导入宿主登录','import-host'):''}${button('撤销连接','revoke-'+p,'danger',db.credentials[p]!=='connected')}</div></div>`).join('')}<small>原型只使用模拟结果，不读取真实凭证。</small></div>`;};
mcp=function(){const authorized=db.credentials.github==='connected';return header('管理','MCP 连接')+managementTabs('mcp')+`<div class="content"><div class="card"><div class="row"><h2>GitHub</h2>${badge(db.mcpEnabled?(authorized?'工具已连接':'需要重新授权'):'工具未连接')}</div><p>账号：${credentialLabels[db.credentials.github]} · 工具：${db.mcpEnabled?'已建立':'未建立'}</p><small>宿主侧 · 示例工具：搜索仓库、读取文件。</small><div class="actions">${!authorized?button('连接 GitHub 账号','auth-github','primary'):''}${button(db.mcpEnabled?'测试工具连接':'建立工具连接',db.mcpEnabled?'mcp-test':'mcp-create',authorized?'primary':'',!authorized)}${button('移除工具连接','mcp-remove','danger',!db.mcpEnabled)}</div></div><div class="card"><h3>Filesystem</h3><small>无凭证 · 沙箱侧 · 仅 workspace</small></div></div>`;};
const dailyWorkspace=workspace;
workspace=function(){let html=dailyWorkspace();const s=runs(ui.workspace).find(active)||run();if(s){let label,act;if(s.status==='running'){label='打开终端';act='open-current';}else if(s.status==='creating'||s.status==='starting'){label='查看启动进度';act='open-current';}else if(s.status==='stopping'){label='查看停止进度';act='open-current';}else{label=s.status==='stopped'?'继续工作':'配置新运行';act='launch';}html=html.replace(/<div class="actions"><button[^>]*data-act="(?:open-current|launch)"[^>]*>[^<]*<\/button><\/div>/,`<div class="actions">${button(label,act,'primary')}</div>`);}return html;};
const dailyStatePage=statePage;
statePage=function(){let html=dailyStatePage();for(const st of db.states){const owner=db.sandboxes.find(s=>active(s)&&s.state===st.id);if(!owner)continue;const actions=`<button data-state-action="reset" data-state-id="${st.id}">清除配置与历史</button><button class="danger" data-state-action="delete" data-state-id="${st.id}">删除 state</button>`;html=html.replace(actions,`<button data-visit-run="${owner.id}">查看占用运行</button><small>使用中，暂不可清理</small>`);}return html;};
const dailyRender=render;let renderedViewKey='';
render=function(){const previousPage=ui.page,key=ui.page+'/'+ui.workspace+'/'+ui.tab,currentFile=renderedViewKey===key&&$('#filename')?{file:$('#filename').textContent,content:$('#filecontent').textContent}:null;dailyRender();renderedViewKey=key;if(currentFile&&$('#filename')){$('#filename').textContent=currentFile.file;$('#filecontent').textContent=currentFile.content;}
 if(ui.page==='workspace'){
  const scope=ws(),s=run();
  if(ui.tab!=='run'&&s){const bar=document.createElement('div');bar.className='task-return row';bar.innerHTML=`<small>当前运行：${s.id} · ${lifecycle[s.status]}。切换页面不会创建新运行。</small>${button(active(s)?'返回此运行终端':'查看此运行', 'refresh-current')}`;$('#main .content')?.prepend(bar);}
  // Keep test fixtures away from the production task path.
  const scenario=$('.scenario');if(scenario){const fold=document.createElement('details');fold.className='scenario-fold';const summary=document.createElement('summary');summary.textContent='原型评审工具';scenario.replaceWith(fold);fold.append(summary,scenario);}
  if(!$('#dialog').open&&previousPage==='workspace'&&scope)projectViews.set(scope.id,{...(projectViews.get(scope.id)||{}),workspace:scope.id,sandbox:ui.selected[scope.id],tab:ui.tab,runTab:ui.runTab,scroll:window.scrollY});
 }
 // Navigation state must remain discoverable with assistive technology.
 for(const b of document.querySelectorAll('#global button,#workspaces button')){if(b.classList.contains('active'))b.setAttribute('aria-current','page');else b.removeAttribute('aria-current');}
 if(!$('#workspaces button'))$('#workspaces').innerHTML='<small class="nav-empty">没有匹配的工作区</small>';
};
// Make ongoing tasks navigable without turning modal closure into cancellation.
const dailyModal=modal;
modal=function(title,body,action){dailyModal(title,body,action);if(title==='资源正在被使用')$('#dialog').querySelector('h2').textContent='先查看正在使用的运行';};
const dailyStyles=document.createElement('link');dailyStyles.rel='stylesheet';dailyStyles.href='daily.css?revision=3';document.head.append(dailyStyles);render();
