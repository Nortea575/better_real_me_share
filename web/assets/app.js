const app=document.querySelector('#app');
const serviceInfo=document.querySelector('#serviceInfo');
const SESSION_KEY='brm-share-admin-session';
const state={info:null,token:'',setupTicket:'',bindingUrl:'',connections:[],bindingLinks:new Map(),errors:new Map(),error:'',notice:'',busy:false,createNameDraft:'',publicCode:'',publicLoading:false,publicPayload:null};

class ApiError extends Error {
  constructor(message,status=0) {super(message);this.name='ApiError';this.status=status;}
}
function element(tag,className='',text='') {
  const node=document.createElement(tag);
  if (className) node.className=className;
  if (text!=='') node.textContent=String(text);
  return node;
}
function button(text,action,className='') {
  const node=element('button',className,text);node.type='button';
  if (action) node.dataset.action=action;
  return node;
}
function messageFrom(data,status) {
  if (data && typeof data.error==='string' && data.error.trim()) return data.error.slice(0,500);
  if (data && typeof data.message==='string' && data.message.trim()) return data.message.slice(0,500);
  return status?`分享服务暂不可用（${status}）。请稍后重试。`:'无法连接分享服务，请检查网络后重试。';
}
async function api(path,{method='GET',body,auth=true}={}) {
  const headers={Accept:'application/json'};
  const token=state.token || sessionStorage.getItem(SESSION_KEY) || '';
  if (auth && token) headers.Authorization=`Bearer ${token}`;
  if (body!==undefined) headers['Content-Type']='application/json';
  let response;
  try {
    response=await fetch(new URL(`api/v1/${path}`,document.baseURI),{
      method,headers,body:body===undefined?undefined:JSON.stringify(body),cache:'no-store',credentials:'same-origin',redirect:'error'
    });
  } catch {
    throw new ApiError('无法连接分享服务，请检查网络后重试。');
  }
  let data=null;
  try {data=await response.json();} catch {data=null;}
  if (!response.ok) {
    if (auth && (response.status===401 || response.status===403)) {
      sessionStorage.removeItem(SESSION_KEY);state.token='';
    }
    throw new ApiError(messageFrom(data,response.status),response.status);
  }
  if (data && typeof data.error==='string' && data.error) throw new ApiError(data.error);
  return data || {};
}
function setServiceInfo() {
  const info=state.info;
  if (!info) {serviceInfo.textContent='分享服务暂不可用';return;}
  const parts=[];
  if (info.public_url) parts.push(String(info.public_url));
  parts.push(info.needs_setup?'需要初始化':'服务可用');
  serviceInfo.textContent=parts.join(' · ') || '分享服务已连接';
}
function replaceHash() {
  history.replaceState(null,'',`${location.pathname}${location.search}`);
}
function parseSecret(name) {
  const raw=location.hash.slice(1);
  if (!raw.startsWith(`${name}=`)) return '';
  try {return decodeURIComponent(raw.slice(name.length+1));} catch {return '';}
}
function showUnavailable(title,description,withRetry=false) {
  app.replaceChildren();
  const section=element('section','unavailable');
  section.append(element('h1','',title),element('p','',description));
  if (withRetry) section.append(button(state.publicLoading?'正在刷新…':'刷新','public-refresh','primary'));
  app.append(section);
}
function showLoading(message='正在读取…') {
  app.replaceChildren();
  const section=element('section','load-state');
  section.append(element('span','load-mark'),element('p','',message));app.append(section);
}
function makeAuthPanel({kind,error='',notice=''}) {
  app.replaceChildren();
  const wrap=element('div','auth-wrap'),panel=element('section','panel');
  const heading=element('header','panel-heading'),copy=element('div');
  const title=kind==='setup'?'初始化管理员':'管理员登录';
  copy.append(element('h1','',title));
  copy.append(element('p','',kind==='setup'?'设置管理分享连接的密码。密码只用于验证，不会写入浏览器存储。':'登录后管理绑定链接、公开范围与分享连接。'));
  heading.append(copy);panel.append(heading);
  if (notice) panel.append(element('p','notice-message',''));
  if (notice) panel.querySelector('.notice-message').textContent=notice;
  if (error) panel.append(element('p','error-message',error));
  const form=element('form','auth-form');form.id=kind==='setup'?'setupForm':'loginForm';
  if (kind==='setup') {
    const password=field('管理员密码','password','setup-password','至少 12 个字符','new-password');
    const confirm=field('再次输入密码','password','setup-confirm','','new-password');
    form.append(password.label,confirm.label);
  } else {
    const password=field('管理员密码','password','login-password','','current-password');
    form.append(password.label);
  }
  const submit=element('button','primary',kind==='setup'?'创建管理员并继续登录':'登录');submit.type='submit';form.append(submit);
  panel.append(form);
  if (kind==='setup') panel.append(element('p','auth-help','密码至少 12 个字符。首次设置链接仅可使用一次，页面不会保存它。'));
  wrap.append(panel);app.append(wrap);
}
function field(labelText,type,id,help='',autocomplete='off') {
  const label=element('label','field',labelText),input=document.createElement('input');
  input.id=id;input.name=id;input.type=type;input.autocomplete=autocomplete;input.required=true;
  if (type==='password' && id==='setup-password') input.minLength=12;
  label.append(input);
  if (help) label.append(element('small','',help));
  return {label,input};
}
function showSetupRequired() {
  app.replaceChildren();
  const wrap=element('div','auth-wrap'),panel=element('section','panel'),heading=element('header','panel-heading'),copy=element('div');
  copy.append(element('h1','', '初始化分享管理'));
  copy.append(element('p','', '此服务尚未设置管理员。请先在服务器运行 setup-link 命令，再打开命令一次性输出的私有设置链接。'));
  heading.append(copy);panel.append(heading);
  panel.append(element('p','auth-help','设置链接包含一次性票据。请勿转发；设置完成后该链接会立即失效。'));
  wrap.append(panel);app.append(wrap);
}
function renderBindRoute(ticket) {
  app.replaceChildren();
  const wrap=element('div','auth-wrap'),panel=element('section','panel'),heading=element('header','panel-heading'),copy=element('div');
  copy.append(element('h1','',ticket?'复制到机器人插件':'绑定链接无效'));
  copy.append(element('p','',ticket?'把这条一次性链接粘贴到插件的公网分享设置中。打开此页不会消耗绑定票据，只有插件提交时才会绑定。':'链接缺少绑定票据，请回到分享管理页重新生成。'));
  heading.append(copy);panel.append(heading);
  if (ticket) {
    const original=new URL(location.href);state.bindingUrl=original.href;
    const label=element('label','field','一次性绑定链接'),input=document.createElement('input');
    input.type='url';input.readOnly=true;input.value=state.bindingUrl;input.setAttribute('aria-label','一次性绑定链接');label.append(input);panel.append(label);
    panel.append(element('p','auth-help','链接 20 分钟内有效，只能由一个插件连接使用。请勿粘贴到公开位置。'));
    const copy=button('复制到机器人插件','copy-binding','primary');copy.classList.add('bind-copy');panel.append(copy);
    const status=element('p','auth-help');status.id='bindCopyStatus';status.setAttribute('role','status');panel.append(status);
  }
  wrap.append(panel);app.append(wrap);
}
async function copyValue(value) {
  try {await navigator.clipboard.writeText(value);return true;}
  catch {return false;}
}

function formatDate(value) {
  if (!value) return '尚未同步';
  const date=new Date(value);
  return Number.isNaN(date.getTime())?String(value):new Intl.DateTimeFormat('zh-CN',{dateStyle:'medium',timeStyle:'short'}).format(date);
}
function connectionName(connection) {return String(connection?.name || '未命名连接');}
function addJoinedLink(parent,title,value,action,copyLabel='复制') {
  if (!value) return;
  const wrap=element('div','connection-link');wrap.append(element('span','',title));
  const joined=element('div','joined-input'),input=document.createElement('input');
  input.type='url';input.readOnly=true;input.value=String(value);input.setAttribute('aria-label',title);
  const copy=button(copyLabel,action);copy.dataset.url=String(value);
  joined.append(input,copy);wrap.append(joined);
  const feedback=element('p','copy-feedback');feedback.setAttribute('role','status');wrap.append(feedback);parent.append(wrap);
}
function renderConnection(connection,index) {
  const article=element('article','connection-row');article.dataset.connectionId=String(connection.id || '');
  const heading=element('header','connection-heading'),title=element('div','connection-title');
  title.append(element('h3','',connectionName(connection)));
  const lastUpdate=connection.last_received_at?`最近更新 ${formatDate(connection.last_received_at)}`:'';
  title.append(element('p','',!connection.paired?'等待插件绑定':lastUpdate || '已连接 · 等待更新'));
  heading.append(title);article.append(heading);
  const actions=element('div','connection-actions');
  if (connection.paired) {
    const switcher=button('', 'visibility','switch-control');
    switcher.setAttribute('role','switch');switcher.setAttribute('aria-checked',String(Boolean(connection.public_enabled)));
    switcher.setAttribute('aria-label',`${connection.public_enabled?'关闭':'开启'}${connectionName(connection)}的公开展示`);
    switcher.disabled=state.busy;
    switcher.append(element('span','switch-track'),element('span','',connection.public_enabled?'公开展示已开启':'公开展示已关闭'));
    switcher.dataset.id=String(connection.id || '');actions.append(switcher);
  }
  const more=element('details','connection-more'),summary=element('summary','','更多'),secondary=element('div','connection-more-actions');
  const renew=button(connection.paired?'重新绑定':'重新生成绑定链接','renew');renew.dataset.id=String(connection.id || '');renew.disabled=state.busy;
  const rotate=button('更换公开链接','rotate');rotate.dataset.id=String(connection.id || '');rotate.disabled=state.busy;
  const remove=button('删除连接','delete','quiet-button danger-button');remove.dataset.id=String(connection.id || '');remove.disabled=state.busy;
  secondary.append(renew);
  if (connection.paired) secondary.append(rotate);
  secondary.append(remove);more.append(summary,secondary);actions.append(more);article.append(actions);
  if (connection.paired) addJoinedLink(article,'公开链接',connection.view_url,'copy-view');
  const freshBinding=state.bindingLinks.get(String(connection.id || ''));
  if (freshBinding) {
    addJoinedLink(article,'新的一次性绑定链接',freshBinding,'copy-binding','复制绑定链接');
    article.append(element('p','connection-meta','链接 20 分钟内有效。'));
  }
  const error=state.errors.get(String(connection.id || ''));
  if (error) article.append(element('p','connection-error',error));
  return article;
}
function createConnectionForm(buttonLabel) {
  const form=element('form','form-row');form.id='createConnectionForm';
  const name=field('备注名称（可选）','text','connection-name');name.input.required=false;name.input.maxLength=120;name.input.placeholder='生活分享';name.input.value=state.createNameDraft;
  form.append(name.label);
  const create=element('button','primary',state.busy?'正在连接…':buttonLabel);create.type='submit';create.disabled=state.busy;form.append(create);
  return form;
}
function renderAdmin() {
  app.replaceChildren();
  const top=element('header','page-heading'),copy=element('div'),actions=element('div','page-heading-actions');
  copy.append(element('h1','', '分享管理'));
  copy.append(element('p','', '生成绑定链接并粘贴到插件设置中。'));
  const logout=button('退出登录','logout','quiet-button');logout.disabled=state.busy;actions.append(logout);top.append(copy,actions);app.append(top);
  if (state.notice) {app.append(element('p','notice-message',state.notice));state.notice='';}
  if (state.error) {app.append(element('p','error-message',state.error));state.error='';}
  const pending=state.connections.find(connection=>!connection.paired);
  const hasPendingBinding=pending && state.bindingLinks.has(String(pending.id));
  if (!state.connections.length) {
    const createPanel=element('section','panel connection-start'),heading=element('header','panel-heading'),headingCopy=element('div');
    headingCopy.append(element('h2','', '连接机器人'));
    headingCopy.append(element('p','', '填写可选备注并获取一次性绑定链接。'));
    heading.append(headingCopy);createPanel.append(heading,createConnectionForm('连接机器人并获取绑定链接'));
    app.append(createPanel);return;
  }
  if (pending && !hasPendingBinding) {
    const createPanel=element('section','panel connection-start'),heading=element('header','panel-heading'),headingCopy=element('div');
    headingCopy.append(element('h2','', '连接机器人'));
    headingCopy.append(element('p','', `继续使用“${connectionName(pending)}”连接。`));
    heading.append(headingCopy);createPanel.append(heading);
    const control=element('div','form-row'),next=button('获取绑定链接','get-binding','primary');
    next.dataset.id=String(pending.id);next.classList.add('connection-primary');next.disabled=state.busy;control.append(next);createPanel.append(control);
    app.append(createPanel);
  }
  const listPanel=element('section','panel'),listHeading=element('header','panel-heading'),listCopy=element('div');
  listCopy.append(element('h2','', '连接'));
  listHeading.append(listCopy);listPanel.append(listHeading);
  const list=element('div','connection-list');
  state.connections.forEach((connection,index)=>list.append(renderConnection(connection,index)));
  listPanel.append(list);
  if (!pending) {
    const more=element('details','connection-add-more'),summary=element('summary','','连接其他机器人');
    more.append(summary,createConnectionForm('获取绑定链接'));listPanel.append(more);
  }
  app.append(listPanel);
}
function updateConnection(next) {
  if (!next || next.id==null) return;
  state.connections=state.connections.map(item=>String(item.id)===String(next.id)?{...item,...next}:item);
}
function showLogin(error='',notice='') {
  state.setupTicket='';state.token='';
  makeAuthPanel({kind:'login',error,notice});
}
function handleUnauthorized(error) {
  if (error instanceof ApiError && (error.status===401 || error.status===403)) {
    showLogin('登录已过期，请重新登录。');return true;
  }
  return false;
}
async function loadConnections() {
  state.error='';state.busy=true;renderAdmin();
  try {
    const result=await api('connections');
    state.connections=Array.isArray(result.connections)?result.connections:[];
    state.busy=false;renderAdmin();
  } catch (error) {
    state.busy=false;
    if (!handleUnauthorized(error)) {state.error=messageFrom({error:error.message},error.status);renderAdmin();}
  }
}
async function startAdmin() {
  state.token=sessionStorage.getItem(SESSION_KEY) || '';
  if (state.info?.needs_setup) {
    state.token='';sessionStorage.removeItem(SESSION_KEY);
    if (state.setupTicket) makeAuthPanel({kind:'setup'});else showSetupRequired();
    return;
  }
  if (state.token) await loadConnections();else showLogin();
}

app.addEventListener('input',event=>{
  if (event.target.id==='connection-name') state.createNameDraft=event.target.value;
});
app.addEventListener('submit',async event=>{
  event.preventDefault();
  const form=event.target;
  if (form.id==='setupForm') {
    const password=form.querySelector('#setup-password'),confirm=form.querySelector('#setup-confirm');
    const pass=password.value,confirmPass=confirm.value;
    if ([...pass].length<12) {makeAuthPanel({kind:'setup',error:'密码至少需要 12 个字符。'});return;}
    if (pass!==confirmPass) {makeAuthPanel({kind:'setup',error:'两次输入的密码不一致。'});return;}
    const submit=form.querySelector('button[type="submit"]');submit.disabled=true;submit.textContent='正在初始化…';
    try {
      await api('setup',{method:'POST',auth:false,body:{setup_token:state.setupTicket,password:pass}});
      state.setupTicket='';replaceHash();
      showLogin('','管理员已初始化，请使用刚设置的密码登录。');
    } catch (error) {
      password.value='';confirm.value='';
      const message=error.message || '初始化失败，请检查一次性设置链接后重试。';
      makeAuthPanel({kind:'setup',error:message});
    }
    return;
  }
  if (form.id==='loginForm') {
    const password=form.querySelector('#login-password'),value=password.value;
    const submit=form.querySelector('button[type="submit"]');submit.disabled=true;submit.textContent='正在登录…';
    try {
      const result=await api('login',{method:'POST',auth:false,body:{password:value}});
      password.value='';
      if (!result.token) throw new ApiError('登录服务没有返回有效会话，请重试。');
      state.token=String(result.token);sessionStorage.setItem(SESSION_KEY,state.token);
      await loadConnections();
    } catch (error) {
      password.value='';
      if (!handleUnauthorized(error)) showLogin(error.message || '登录失败，请检查密码后重试。');
    }
    return;
  }
  if (form.id==='createConnectionForm') {
    const input=form.querySelector('#connection-name'),name=input.value.trim() || '生活分享';
    state.busy=true;state.error='';renderAdmin();
    try {
      const result=await api('connections',{method:'POST',body:{name}});
      const connection=result.connection;
      if (!connection?.id) throw new ApiError('分享服务没有返回新连接，请刷新后检查。');
      state.connections=[...state.connections,connection];
      let bindingUrl=result.binding_url;
      if (!bindingUrl) {
        const issued=await api(`connections/${encodeURIComponent(connection.id)}/binding`,{method:'POST',body:{}});
        bindingUrl=issued.binding_url;
        if (issued.connection) updateConnection(issued.connection);
      }
      if (!bindingUrl) throw new ApiError('绑定链接暂不可用，请重新获取。');
      state.bindingLinks.set(String(connection.id),String(bindingUrl));
      state.createNameDraft='';state.notice='';
    } catch (error) {
      if (handleUnauthorized(error)) return;
      state.error=error.message || '创建连接失败，请重试。';
    } finally {state.busy=false;renderAdmin();}
  }
});

app.addEventListener('click',async event=>{
  const target=event.target.closest('[data-action]');
  if (!target) return;
  const action=target.dataset.action,id=String(target.dataset.id || target.closest('.connection-row')?.dataset.connectionId || '');
  if (action==='copy-binding') {
    const value=state.bindingLinks.get(id);
    if (!value) return;
    const input=target.closest('.connection-link')?.querySelector('input');
    let copied=await copyValue(value);
    if (!copied && input) {input.focus();input.select();try {copied=document.execCommand('copy');} catch {copied=false;}}
    const feedback=target.closest('.connection-link')?.querySelector('.copy-feedback');
    if (feedback) feedback.textContent=copied?'一次性链接已复制；请粘贴到插件设置中。':'链接已选中，请按 Ctrl+C 手动复制。';
    return;
  }
  if (action==='copy-view') {
    const value=target.dataset.url;
    if (value) {
      const input=target.closest('.connection-link')?.querySelector('input');
      let copied=await copyValue(value);
      if (!copied && input) {input.focus();input.select();try {copied=document.execCommand('copy');} catch {copied=false;}}
      const feedback=target.closest('.connection-link')?.querySelector('.copy-feedback');
      if (feedback) feedback.textContent=copied?'公开链接已复制。':'链接已选中，请按 Ctrl+C 手动复制。';
    }
    return;
  }
  if (action==='logout') {
    state.busy=true;target.disabled=true;
    try {await api('logout',{method:'POST',body:{}});} catch {}
    sessionStorage.removeItem(SESSION_KEY);showLogin('','已退出登录。');return;
  }
  const connection=state.connections.find(item=>String(item.id)===id);
  if (!connection || state.busy) return;
  state.busy=true;state.errors.delete(id);renderAdmin();
  try {
    if (action==='get-binding') {
      const result=await api(`connections/${encodeURIComponent(id)}/binding`,{method:'POST',body:{}});
      if (result.connection) updateConnection(result.connection);
      if (!result.binding_url) throw new ApiError('绑定链接暂不可用，请重试。');
      state.bindingLinks.set(id,String(result.binding_url));state.notice='';
    } else if (action==='visibility') {
      const result=await api(`connections/${encodeURIComponent(id)}/visibility`,{method:'POST',body:{enabled:!connection.public_enabled}});
      updateConnection(result);state.notice=result.public_enabled?'公开页面已开启。':'公开页面已关闭。';
    } else if (action==='renew') {
      const result=await api(`connections/${encodeURIComponent(id)}/binding`,{method:'POST',body:{}});
      if (result.connection) updateConnection(result.connection);
      if (result.binding_url) state.bindingLinks.set(id,String(result.binding_url));
      state.notice=connection.paired?'请在插件中重新绑定。':'';
    } else if (action==='rotate') {
      if (!window.confirm(`更换“${connectionName(connection)}”的公开链接？旧链接会立即失效。`)) {state.busy=false;renderAdmin();return;}
      const result=await api(`connections/${encodeURIComponent(id)}/rotate`,{method:'POST',body:{}});
      updateConnection(result);state.notice='公开链接已更换，旧链接立即返回 404。';
    } else if (action==='delete') {
      if (!window.confirm(`删除“${connectionName(connection)}”及其分享数据？此操作无法撤销。`)) {state.busy=false;renderAdmin();return;}
      await api(`connections/${encodeURIComponent(id)}`,{method:'DELETE'});
      state.connections=state.connections.filter(item=>String(item.id)!==id);
      state.bindingLinks.delete(id);state.errors.delete(id);state.notice='连接已删除。';
    }
  } catch (error) {
    if (handleUnauthorized(error)) return;
    state.errors.set(id,error.message || '操作失败，请重试。');
  } finally {state.busy=false;renderAdmin();}
});

function avatar(actor,small=false) {
  const name=String(actor?.name || '');
  const initial=[...name.trim()][0] || '生';
  const fallback=element('span',`avatar avatar-fallback${small?' avatar-small':''}`,initial);
  const raw=String(actor?.avatar_url || '');
  if (!raw) return fallback;
  try {
    const url=new URL(raw,location.href);
    if (!['http:','https:'].includes(url.protocol)) return fallback;
    const image=document.createElement('img');image.className=`avatar${small?' avatar-small':''}`;
    image.alt=`${name || '角色'}头像`;image.loading='lazy';image.referrerPolicy='no-referrer';
    image.addEventListener('error',()=>image.replaceWith(fallback),{once:true});image.src=url.href;
    return image;
  } catch {return fallback;}
}
function formatValue(item) {
  const value=item?.value;
  if (typeof value==='number' && Number.isFinite(value)) return `${new Intl.NumberFormat('zh-CN',{maximumFractionDigits:2}).format(value)}${item.unit?` ${item.unit}`:''}`;
  if (typeof value==='boolean') return value?'是':'否';
  return String(value ?? '—');
}
function boundedPercent(value,min,max) {
  const number=Number(value),low=Number(min),high=Number(max);
  if (![number,low,high].every(Number.isFinite) || high<=low) return null;
  return Math.max(0,Math.min(100,(number-low)/(high-low)*100));
}
function makeMeter(percent,item) {
  const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');
  svg.classList.add('state-bar');svg.setAttribute('viewBox','0 0 100 8');svg.setAttribute('preserveAspectRatio','none');
  svg.setAttribute('role','meter');svg.setAttribute('aria-valuemin',String(item.min));svg.setAttribute('aria-valuemax',String(item.max));
  svg.setAttribute('aria-valuenow',String(item.value));svg.setAttribute('aria-label',String(item.name || '状态'));
  const track=document.createElementNS('http://www.w3.org/2000/svg','rect');track.classList.add('state-bar-track');track.setAttribute('x','0');track.setAttribute('y','0');track.setAttribute('width','100');track.setAttribute('height','8');track.setAttribute('rx','4');
  const fill=document.createElementNS('http://www.w3.org/2000/svg','rect');fill.classList.add('state-bar-fill');fill.setAttribute('x','0');fill.setAttribute('y','0');fill.setAttribute('width',String(percent));fill.setAttribute('height','8');fill.setAttribute('rx','4');
  svg.append(track,fill);return svg;
}
function renderStatePanel(states) {
  const panel=element('section','public-panel');panel.append(element('h2','', '生活状态'));
  panel.append(element('p','panel-lead','按状态分类查看当前值与阶段。'));
  if (!Array.isArray(states) || !states.length) {panel.append(element('p','empty-state','当前没有可公开的状态。'));return panel;}
  const groups=new Map();
  for (const item of states) {
    const category=String(item?.category || '其他状态');
    if (!groups.has(category)) groups.set(category,[]);
    groups.get(category).push(item);
  }
  const body=element('div','state-groups');
  let stateIndex=0;
  for (const [category,items] of groups) {
    const group=element('section','state-group');group.append(element('h3','',category));
    for (const item of items) {
      const detailKey='state-'+stateIndex;stateIndex+=1;
      const row=element('div','state-row'),name=element('span','state-name',item?.name || '未命名状态');
      const valueVisible=item?.show_value!==false;
      const width=valueVisible && String(item?.type)==='number'?boundedPercent(item.value,item.min,item.max):null;
      row.append(name);
      if (width!==null) {
        row.append(makeMeter(width,item));
      } else row.classList.add('state-row-text');
      const visibleValue=valueVisible?(item?.stage || formatValue(item)):'值已隐藏';
      row.append(element('span','state-value',visibleValue));
      if (valueVisible) {
        row.classList.add('has-details');
        const details=element('details','state-details'),content=element('div','state-detail-content');
        details.dataset.publicDetail=detailKey;
        details.append(element('summary','','说明'));
        content.append(element('strong','state-detail-value','当前值：'+formatValue(item)));
        if (item?.stage) content.append(element('span','state-detail-stage','当前阶段：'+String(item.stage)));
        if (item?.stage_description) content.append(element('p','state-description','说明：'+String(item.stage_description)));
        details.append(content);row.append(details);
      }
      group.append(row);
    }
    body.append(group);
  }
  panel.append(body);return panel;
}
function timeText(value,timezone='') {
  if (!value) return '—';
  const date=new Date(value);if (Number.isNaN(date.getTime())) return String(value);
  try {
    const options={hour:'2-digit',minute:'2-digit'};
    if (timezone) options.timeZone=timezone;
    return new Intl.DateTimeFormat('zh-CN',options).format(date);
  } catch {return new Intl.DateTimeFormat('zh-CN',{hour:'2-digit',minute:'2-digit'}).format(date);}
}
function appendScheduleEntry(list,item,timezone) {
  const row=element('div','schedule-entry'),copy=element('div','schedule-entry-copy');
  copy.append(element('strong','',item?.name || '未命名安排'));
  const details=[item?.category,item?.description].filter(Boolean).map(String);
  if (details.length) copy.append(element('span','',details.join(' · ')));
  const time=element('span','schedule-entry-time',`${timeText(item?.start_at,timezone)} – ${timeText(item?.end_at,timezone)}`);
  row.append(copy,time);list.append(row);
}
function renderSchedulePanel(schedule) {
  const panel=element('section','public-panel schedule-panel');panel.append(element('h2','', '日程与生活时段'));
  panel.append(element('p','panel-lead','固定日程和生活预设时段分别显示。'));
  const summary=element('div','schedule-summary');
  const active=element('div','schedule-summary-item');active.append(element('span','', '当前固定日程'),element('strong','',schedule?.active?.name || '暂无进行中的固定日程'));
  const next=element('div','schedule-summary-item');next.append(element('span','', '下一固定日程'),element('strong','',schedule?.next?.name?`${schedule.next.name} · ${timeText(schedule.next.start_at,schedule.timezone)}`:'暂无后续固定日程'));
  summary.append(active,next);panel.append(summary);
  const details=element('details','schedule-details');
  details.dataset.publicDetail='schedule';
  details.append(element('summary','','查看完整固定日程与生活时段'));
  const fixed=Array.isArray(schedule?.entries)?schedule.entries:[];
  const fixedHeading=element('div','schedule-list-heading');fixedHeading.append(element('h3','', '固定日程'));details.append(fixedHeading);
  const fixedList=element('div','schedule-list');
  if (!fixed.length) fixedList.append(element('p','empty-state','当前没有已公开的固定日程。'));
  else fixed.forEach(item=>appendScheduleEntry(fixedList,item,schedule?.timezone));
  details.append(fixedList);
  const windows=Array.isArray(schedule?.windows)?schedule.windows:[];
  const lifeHeading=element('div','schedule-list-heading');lifeHeading.append(element('h3','', '生活时段'));details.append(lifeHeading);
  const windowList=element('div','schedule-list');
  if (!windows.length) windowList.append(element('p','empty-state','当前没有已公开的生活时段。'));
  else windows.forEach(item=>appendScheduleEntry(windowList,item,schedule?.timezone));
  details.append(windowList);panel.append(details);return panel;
}
function roomLabel(id,rooms) {return rooms.find(room=>String(room.id)===String(id))?.name || String(id || '未知房间');}
function renderRoom(room,locationData,actor,rooms) {
  const row=element('li',`location-room${String(room.id)===String(locationData?.room_id)?' is-current':''}`);
  if (String(room.id)===String(locationData?.room_id)) row.append(avatar(actor,true));
  row.append(element('span','',room.name || '未命名房间'));
  if (String(room.id)===String(locationData?.room_id)) row.append(element('span','','· 当前位置'));
  return row;
}
function renderLocationTree(locationData,actor) {
  const list=element('ul','location-tree'),groups=Array.isArray(locationData?.groups)?locationData.groups:[],rooms=Array.isArray(locationData?.rooms)?locationData.rooms:[];
  const children=new Map();
  for (const group of groups) {
    const parent=String(group?.parent_id || '');
    if (!children.has(parent)) children.set(parent,[]);
    children.get(parent).push(group);
  }
  const visiting=new Set();
  const buildGroup=group=>{
    const id=String(group?.id || ''),item=element('li','location-group');
    item.append(element('span','location-group-head',group?.name || '未命名空间'));
    const nested=element('div','location-group-children');
    for (const room of rooms.filter(candidate=>String(candidate?.group_id || '')===id)) nested.append(renderRoom(room,locationData,actor,rooms));
    if (!visiting.has(id)) {
      visiting.add(id);
      for (const child of children.get(id) || []) nested.append(buildGroup(child));
      visiting.delete(id);
    }
    if (!nested.childNodes.length) nested.append(element('span','location-room','没有房间'));
    item.append(nested);return item;
  };
  const roots=children.get('') || [];
  roots.forEach(group=>list.append(buildGroup(group)));
  if (!roots.length && rooms.length) rooms.forEach(room=>list.append(renderRoom(room,locationData,actor,rooms)));
  if (!list.childNodes.length) list.append(element('li','empty-state','当前没有可公开的空间。'));
  return list;
}
function renderLocationMap(locationData,actor) {
  const rooms=Array.isArray(locationData?.rooms)?locationData.rooms:[];
  const currentId=String(locationData?.room_id || '');
  const currentRoom=rooms.find(room=>String(room.id)===currentId);
  const groupId=String(currentRoom?.group_id || locationData?.group_id || '');
  let groupRooms=rooms.filter(room=>!groupId || String(room.group_id || '')===groupId);
  if (currentId && !groupRooms.some(room=>String(room.id)===currentId)) {
    groupRooms.unshift({id:currentId,name:roomLabel(currentId,rooms),group_id:groupId});
  }
  if (!groupRooms.length) return null;
  const edges=Array.isArray(locationData?.connections)?locationData.connections:[];
  const nearby=edges.flatMap(edge=>{
    if (String(edge?.a)===currentId) return [String(edge?.b || '')];
    if (String(edge?.b)===currentId) return [String(edge?.a || '')];
    return [];
  });
  const ordered=[],seenRooms=new Set();
  for (const id of [currentId,...nearby,...groupRooms.map(room=>String(room.id || ''))]) {
    const room=groupRooms.find(candidate=>String(candidate.id || '')===id);
    if (room && !seenRooms.has(id)) {ordered.push(room);seenRooms.add(id);}
  }
  const visibleRooms=ordered.slice(0,12),truncated=ordered.length>visibleRooms.length;
  const columns=4,nodeWidth=78,nodeHeight=36,x0=8,xGap=86,y0=10,yGap=55;
  const rows=Math.ceil(visibleRooms.length/columns),height=56+(rows-1)*yGap;
  const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');
  svg.classList.add('location-map');svg.setAttribute('viewBox','0 0 360 '+height);
  svg.setAttribute('role','img');svg.setAttribute('aria-label','当前位置所在空间的房间连接示意图');
  const pointById=new Map();
  visibleRooms.forEach((room,index)=>{
    const x=x0+(index%columns)*xGap,y=y0+Math.floor(index/columns)*yGap;
    pointById.set(String(room.id || ''),{x:x+nodeWidth/2,y:y+nodeHeight/2});
  });
  const drawnEdges=new Set();
  for (const edge of edges) {
    const a=String(edge?.a || ''),b=String(edge?.b || ''),start=pointById.get(a),end=pointById.get(b);
    if (!start || !end || a===b) continue;
    const key=[a,b].sort().join('|');if (drawnEdges.has(key)) continue;drawnEdges.add(key);
    const line=document.createElementNS('http://www.w3.org/2000/svg','line');
    line.classList.add('location-map-edge');line.setAttribute('x1',start.x);line.setAttribute('y1',start.y);line.setAttribute('x2',end.x);line.setAttribute('y2',end.y);svg.append(line);
  }
  const actorName=String(actor?.name || ''),initial=[...actorName.trim()][0] || '生';
  let avatarUrl='';
  const rawAvatar=String(actor?.avatar_url || '').trim();
  if (rawAvatar) {
    try {
      const parsed=new URL(rawAvatar,location.href);
      if (['http:','https:'].includes(parsed.protocol)) avatarUrl=parsed.href;
    } catch {}
  }
  const shortName=(value,limit=5)=>{const chars=[...String(value || '未命名房间')];return chars.length>limit?chars.slice(0,limit).join('')+'…':chars.join('');};
  visibleRooms.forEach((room,index)=>{
    const id=String(room.id || ''),current=id===currentId,x=x0+(index%columns)*xGap,y=y0+Math.floor(index/columns)*yGap;
    const node=document.createElementNS('http://www.w3.org/2000/svg','g');
    node.classList.add('location-map-node');if (current) node.classList.add('is-current');
    node.setAttribute('transform','translate('+x+' '+y+')');node.setAttribute('role','group');
    node.setAttribute('aria-label',String(room.name || '未命名房间')+(current?' · 当前位置':''));
    if (current) node.setAttribute('aria-current','location');
    const title=document.createElementNS('http://www.w3.org/2000/svg','title');
    title.textContent=String(room.name || '未命名房间')+(current?' · '+actorName+'的当前位置':'');
    const rect=document.createElementNS('http://www.w3.org/2000/svg','rect');
    rect.setAttribute('width',nodeWidth);rect.setAttribute('height',nodeHeight);rect.setAttribute('rx','9');node.append(title,rect);
    let labelX=18;
    const appendFallbackAvatar=()=>{
      const marker=document.createElementNS('http://www.w3.org/2000/svg','circle');
      marker.classList.add('location-map-avatar-fallback');marker.setAttribute('cx','17');marker.setAttribute('cy','18');marker.setAttribute('r','11');
      const text=document.createElementNS('http://www.w3.org/2000/svg','text');
      text.classList.add('location-map-initial');text.setAttribute('x','17');text.setAttribute('y','22');text.setAttribute('text-anchor','middle');text.textContent=initial;
      node.append(marker,text);
    };
    if (current && avatarUrl) {
      const image=document.createElementNS('http://www.w3.org/2000/svg','image');
      image.classList.add('location-map-avatar');image.setAttribute('href',avatarUrl);image.setAttribute('x','6');image.setAttribute('y','7');image.setAttribute('width','22');image.setAttribute('height','22');image.setAttribute('preserveAspectRatio','xMidYMid slice');image.setAttribute('referrerpolicy','no-referrer');
      image.addEventListener('error',()=>{image.remove();appendFallbackAvatar();},{once:true});
      node.append(image);labelX=32;
    } else if (current) {
      appendFallbackAvatar();labelX=32;
    } else {
      const marker=document.createElementNS('http://www.w3.org/2000/svg','circle');
      marker.classList.add('location-map-dot');
      marker.setAttribute('cx','10');marker.setAttribute('cy','18');marker.setAttribute('r','3');node.append(marker);
    }
    const label=document.createElementNS('http://www.w3.org/2000/svg','text');
    label.classList.add('location-map-label');label.setAttribute('x',labelX);label.setAttribute('y','22');label.textContent=shortName(room.name,current?4:5);
    node.append(label);svg.append(node);
  });
  const wrap=element('div','location-map-wrap');
  const group=Array.isArray(locationData?.groups)?locationData.groups.find(item=>String(item.id)===groupId):null;
  wrap.append(element('p','location-map-caption',group?.name?group.name+' · 当前房间连接': '当前房间连接'),svg);
  if (truncated) wrap.append(element('p','location-map-note','仅显示当前空间的部分房间，完整结构可展开查看。'));
  return wrap;
}
function renderLocationPanel(locationData,actor) {
  const panel=element('section','public-panel location-panel');panel.append(element('h2','', '当前位置'));
  const current=element('div','location-current');current.append(avatar(actor));
  const copy=element('div','location-current-copy');
  copy.append(element('strong','',roomLabel(locationData?.room_id,locationData?.rooms || [])));
  copy.append(element('span','',locationData?.path || '暂未提供当前位置路径'));
  current.append(copy);panel.append(current);
  const map=renderLocationMap(locationData,actor);
  if (map) panel.append(map);
  const details=element('details','location-details');
  details.dataset.publicDetail='location';
  details.append(element('summary','','查看完整空间结构与连接'));
  details.append(renderLocationTree(locationData,actor));
  const connections=Array.isArray(locationData?.connections)?locationData.connections:[];
  if (connections.length) {
    const section=element('section','location-connections');section.append(element('h3','', '相连空间'));
    for (const edge of connections) section.append(element('p','',`${roomLabel(edge?.a,locationData.rooms || [])} ↔ ${roomLabel(edge?.b,locationData.rooms || [])}`));
    details.append(section);
  }
  const exits=Array.isArray(locationData?.exits)?locationData.exits:[];
  if (exits.length) {
    const section=element('section','location-connections');section.append(element('h3','', '可前往的位置'));
    for (const exit of exits) section.append(element('p','',exit?.path || exit?.name || '空间出口'));
    details.append(section);
  }
  panel.append(details);
  return panel;
}
function renderActivityPanel(snapshot) {
  const panel=element('section','public-panel');panel.append(element('h2','', '正在进行'));
  const life=snapshot?.life || {},activity=life.activity;
  if (life.paused) {panel.append(element('p','activity-empty','自动生活已暂停。公开页面仍会保留最近一次同步的数据。'));return panel;}
  if (!activity) {panel.append(element('p','activity-empty','当前没有正在进行的生活活动。'));return panel;}
  const body=element('div','activity-content');
  body.append(element('strong','activity-name',activity.name || '生活活动'));
  if (activity.step_name) body.append(element('p','activity-step',activity.step_name));
  if (activity.step_count>1 && Number.isFinite(Number(activity.step_index))) body.append(element('p','activity-step',`阶段 ${Number(activity.step_index)} / ${Number(activity.step_count)}`));
  if (activity.ends_at) body.append(element('p','activity-step',`预计结束 ${formatDate(activity.ends_at)}`));
  if (activity.target_path) body.append(element('p','activity-path',activity.target_path));
  panel.append(body);return panel;
}
function renderPublic(payload) {
  const snapshot=payload?.snapshot;
  if (!snapshot) {showUnavailable('分享数据正在准备中','点刷新后读取最新状态。',true);return;}
  const openDetails=new Set([...app.querySelectorAll('details[data-public-detail]')].filter(item=>item.open).map(item=>item.dataset.publicDetail));
  app.replaceChildren();
  const actor=snapshot.actor || {},connection=payload.connection || {};
  const intro=element('header','public-intro');intro.append(avatar(actor));
  const copy=element('div','public-intro-copy');
  const actorName=String(actor.name || connection.name || '生活角色');
  copy.append(element('h1','',`${actorName} 的生活状态`));copy.append(element('p','',connection.name?`分享连接：${connection.name}`:'只读生活快照'));
  const refresh=button(state.publicLoading?'正在刷新…':'刷新','public-refresh','quiet-button');refresh.disabled=state.publicLoading;refresh.setAttribute('aria-label','刷新公开数据');
  intro.append(copy,refresh);app.append(intro);
  const status=element('div',`public-status${snapshot.life?.paused?' is-paused':''}${payload.read_error?' is-refresh-error':''}`);
  if (snapshot.life?.paused) status.append(element('strong','', '自动生活已暂停'));
  status.append(element('span','',`上次更新：${formatDate(payload.received_at)}`));
  if (payload.refreshed===false) status.append(element('span','','暂未取得新数据'));
  if (payload.read_error) status.append(element('span','','刷新失败，显示上次数据'));
  app.append(status);
  const overview=element('div','public-overview');
  overview.append(renderActivityPanel(snapshot));
  if (snapshot.sections?.location!==false && snapshot.location) overview.append(renderLocationPanel(snapshot.location,actor));
  if (snapshot.sections?.schedule!==false && snapshot.schedule) overview.append(renderSchedulePanel(snapshot.schedule));
  app.append(overview);
  if (snapshot.sections?.states!==false) app.append(renderStatePanel(Array.isArray(snapshot.states)?snapshot.states:[]));
  for (const details of app.querySelectorAll('details[data-public-detail]')) details.open=openDetails.has(details.dataset.publicDetail);
}
async function refreshPublic() {
  const requestedCode=state.publicCode;
  if (!requestedCode || state.publicLoading) return;
  state.publicLoading=true;
  const control=app.querySelector('[data-action="public-refresh"]');
  if (control) {control.disabled=true;control.textContent='正在刷新…';}
  try {
    const payload=await api(`public/${encodeURIComponent(requestedCode)}`,{auth:false});
    if (requestedCode!==state.publicCode) return;
    if (payload?.snapshot===null && payload?.received_at===null) {
      state.publicPayload=payload;showUnavailable('分享数据正在准备中','点刷新后读取最新状态。',true);return;
    }
    state.publicPayload=payload;renderPublic(payload);
  } catch (error) {
    if (requestedCode!==state.publicCode) return;
    if (error.status===404) {
      state.publicPayload=null;showUnavailable('这个分享链接暂不可用','点刷新重试，或确认链接仍在公开。',true);
    } else if (state.publicPayload?.snapshot) renderPublic({...state.publicPayload,read_error:true});
    else showUnavailable('暂时无法读取分享内容','网络暂时不可用，点刷新重试。',true);
  } finally {
    if (requestedCode===state.publicCode) {
      state.publicLoading=false;
      const current=app.querySelector('[data-action="public-refresh"]');
      if (current) {current.disabled=false;current.textContent='刷新';}
    }
  }
}
function startPublic(code) {
  state.publicCode=code;state.publicPayload=null;showLoading('正在读取公开生活数据…');
  void refreshPublic();
}
function route() {
  state.publicCode='';state.publicLoading=false;
  const hash=location.hash;
  if (hash.startsWith('#/view/')) {
    const code=hash.slice('#/view/'.length);
    if (!/^[A-Za-z0-9_-]{1,256}$/.test(code)) {showUnavailable('分享链接格式不正确','请检查链接是否完整。');return;}
    startPublic(code);return;
  }
  if (hash.startsWith('#bind=')) {renderBindRoute(parseSecret('bind'));return;}
  if (hash) {showUnavailable('页面地址无效','请使用分享管理页生成的设置、绑定或公开链接。');return;}
  void startAdmin();
}
app.addEventListener('click',async event=>{
  const target=event.target.closest('[data-action="copy-binding"],[data-action="public-refresh"],[data-action="get-binding"]');
  if (!target) return;
  if (target.dataset.action==='public-refresh') {await refreshPublic();return;}
  if (target.dataset.action==='get-binding') {
    const id=String(target.dataset.id || '');
    if (!id || state.busy) return;
    state.busy=true;state.error='';renderAdmin();
    try {
      const result=await api(`connections/${encodeURIComponent(id)}/binding`,{method:'POST',body:{}});
      if (result.connection) updateConnection(result.connection);
      if (!result.binding_url) throw new ApiError('绑定链接暂不可用，请重试。');
      state.bindingLinks.set(id,String(result.binding_url));state.notice='';
    } catch (error) {
      if (handleUnauthorized(error)) return;
      state.errors.set(id,error.message || '获取绑定链接失败，请重试。');
    } finally {state.busy=false;renderAdmin();}
    return;
  }
  const input=app.querySelector('input[aria-label="一次性绑定链接"]');
  if (!input || !state.bindingUrl) return;
  const status=app.querySelector('#bindCopyStatus');
  let copied=await copyValue(state.bindingUrl);
  if (!copied) {input.focus();input.select();try {copied=document.execCommand('copy');} catch {copied=false;}}
  if (copied) {
    replaceHash();state.bindingUrl='';input.value='';target.disabled=true;target.textContent='已复制到剪贴板';
    if (status) status.textContent='请回到机器人插件的公网分享设置并粘贴。此页面不会提交或消费绑定票据。';
  } else {
    input.focus();input.select();
    if (status) status.textContent='浏览器未允许自动复制；链接已选中，请按 Ctrl+C 复制。';
  }
});

async function initialize() {
  if (location.hash.startsWith('#setup=')) {
    state.setupTicket=parseSecret('setup');replaceHash();
  }
  try {
    state.info=await api('info',{auth:false});setServiceInfo();
  } catch (error) {
    state.info=null;setServiceInfo();showUnavailable('分享服务无法连接',error.message || '请稍后重试。');return;
  }
  route();
}
void initialize();
