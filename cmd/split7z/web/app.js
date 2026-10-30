(() => {
'use strict';
const token=document.querySelector('meta[name="split-token"]').content;
const $=id=>document.getElementById(id);
let state=null,selected=new Set(),chartMode='donut',busy=false,pollBusy=false,initial=true,lastLogs='',lastEntries='',showToastTimer=0,connectionFailures=0;
const COLORS=['#318cf8','#5a9dfa','#9365f7','#ffab69','#42c9aa','#12acd6','#ef7aaf','#8396bb'];
const fmt=n=>{n=Number(n)||0;if(n<1024)return `${n} B`;let i=0;for(const u of ['KiB','MiB','GiB','TiB']){i++;if(n<1024**(i+1))return `${(n/1024**i).toFixed(i>=3?2:1).replace(/\.0$/,'')} ${u}`;}return `${(n/1024**5).toFixed(2)} PiB`};
const pad=n=>String(n).padStart(2,'0');
const duration=n=>`${pad(Math.floor(n/3600))}:${pad(Math.floor(n/60)%60)}:${pad(Math.floor(n)%60)}`;
const el=(tag,cls,text)=>{const x=document.createElement(tag);if(cls)x.className=cls;if(text!==undefined)x.textContent=text;return x};
const icon=name=>{const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');const use=document.createElementNS(svg.namespaceURI,'use');use.setAttribute('href','#i-'+name);svg.appendChild(use);return svg};
function toast(message){const t=$('toast');t.textContent=message;t.hidden=false;clearTimeout(showToastTimer);showToastTimer=setTimeout(()=>t.hidden=true,5000)}
function modal(title,lines){$('modalTitle').textContent=title;$('modalContent').textContent=lines;$('modal').hidden=false}
for(const id of ['closeModal','okModal'])$(id).addEventListener('click',()=>$('modal').hidden=true);
async function api(path,payload={},method='POST'){
 const result=await fetch('/api/'+path,{method,headers:{'X-Split-Token':token,'Content-Type':'application/json'},body:method==='GET'?undefined:JSON.stringify(payload)});
 const data=await result.json().catch(()=>({error:'请求失败'}));
 if(!result.ok)throw new Error(data.error||`操作失败 (${result.status})`);
 return data;
}
async function action(path,payload={}){
 if(busy)return;busy=true;
 try{await api(path,payload);await refresh(true)}catch(e){toast(e.message)}finally{busy=false}
}
function currentParams(){return {outputDir:$('outputDir').value.trim(),archiveName:$('archiveName').value.trim(),volumeMiB:Number($('volumeSize').value),level:Number($('level').value),format:$('format').value,verify:$('verify').checked}}
function updateFiles(inputs){
 const sig=JSON.stringify(inputs.map(x=>[x.path,x.size,state?.running]));if(sig===lastEntries)return;lastEntries=sig;
 const body=$('filesBody');body.replaceChildren();
 if(!inputs.length){body.innerHTML='<tr class="empty-row"><td colspan="5"><div class="empty-illustration"><svg><use href="#i-folder"/></svg></div><strong>还没有待压缩项目</strong><span>点击“添加文件”或“添加文件夹”开始</span></td></tr>';}
 for(const item of inputs){
  const row=el('tr');
  const checkTd=el('td','check-cell'),box=el('input');box.type='checkbox';box.checked=selected.has(item.path);box.disabled=state?.running||false;box.addEventListener('change',()=>{if(box.checked)selected.add(item.path);else selected.delete(item.path);syncChecks()});checkTd.appendChild(box);row.appendChild(checkTd);
  const nameTd=el('td'),itemName=el('div','item-name'),badge=el('span',item.type==='文件夹'?'file-badge folder':'file-badge');badge.appendChild(icon(item.type==='文件夹'?'folder':'file'));itemName.append(badge,el('span','filename',item.name));nameTd.append(itemName);nameTd.title=item.path;row.appendChild(nameTd);
  const pathTd=el('td','path-cell',item.directory);pathTd.title=item.directory;row.appendChild(pathTd);
  const sizeTd=el('td','size-cell',fmt(item.size));row.appendChild(sizeTd);
  row.appendChild(el('td','type-cell',item.type));body.appendChild(row);
 }
 $('fileCount').innerHTML=`已添加 <b>${inputs.length}</b> 项`;
 selected=new Set([...selected].filter(x=>inputs.some(e=>e.path===x)));
 syncChecks();
}
function syncChecks(){const all=state?.inputs||[];$('selectAll').checked=all.length>0&&selected.size===all.length;$('selectAll').indeterminate=selected.size>0&&selected.size<all.length;$('removeSelected').disabled=selected.size===0||!!state?.running}
function chartParts(volumes){
 if(volumes.length<=6)return volumes.map(x=>({...x}));
 return [...volumes.slice(0,5),{name:`其余 ${volumes.length-5} 卷`,size:volumes.slice(5).reduce((a,b)=>a+b.size,0)}];
}
function renderDonut(parts,total){
 const svg=$('donutSvg');svg.replaceChildren();const cx=116,cy=116,r=88,circ=2*Math.PI*r;
 if(total===0){const c=document.createElementNS(svg.namespaceURI,'circle');c.setAttribute('cx',cx);c.setAttribute('cy',cy);c.setAttribute('r',r);c.setAttribute('stroke','#e8eff7');c.setAttribute('stroke-width',43);c.setAttribute('fill','none');svg.append(c);return;}
 let off=0;
 for(let i=0;i<parts.length;i++){
  const length=parts[i].size/total*circ;
  const circle=document.createElementNS(svg.namespaceURI,'circle');circle.setAttribute('cx',cx);circle.setAttribute('cy',cy);circle.setAttribute('r',r);circle.setAttribute('fill','none');circle.setAttribute('stroke',COLORS[i%COLORS.length]);circle.setAttribute('stroke-width',45);circle.setAttribute('stroke-linecap','butt');
  circle.setAttribute('stroke-dasharray',`${Math.max(0,length-1)} ${circ-Math.max(0,length-1)}`);circle.setAttribute('stroke-dashoffset',-off);circle.style.transition='stroke-dasharray .3s';svg.append(circle);off+=length;
 }
}
function renderMiniDistribution(volumes){
 const total=volumes.reduce((n,v)=>n+v.size,0), mini=$('miniDistribution');
 mini.replaceChildren();
 if(!total){mini.append(el('span','empty-fill'));return}
 const parts=chartParts(volumes);
 for(let i=0;i<parts.length;i++){
  const seg=el('span');seg.style.width=`${parts[i].size/total*100}%`;
  seg.style.background=COLORS[i%COLORS.length];
  seg.title=`${parts[i].name} · ${fmt(parts[i].size)} · ${(parts[i].size/total*100).toFixed(1)}%`;
  mini.append(seg);
 }
}
function renderCharts(volumes){
 renderMiniDistribution(volumes);
 const parts=chartParts(volumes),total=volumes.reduce((n,v)=>n+v.size,0);
 $('chartCount').textContent=`${volumes.length} 卷`;$('chartTotal').textContent=`共 ${fmt(total)}`;
 renderDonut(parts,total);
 const legend=$('chartLegend');legend.replaceChildren();
 if(!total){legend.append(el('div','no-chart','压缩后将在这里展示每一卷的大小'));}
 else parts.forEach((v,i)=>{const row=el('div','legend-item'),dot=el('span','legend-dot');dot.style.background=COLORS[i%COLORS.length];const label=v.name.startsWith('其余')?v.name:`第 ${i+1} 卷`;const dotLabel=el('span','legend-label',label);dotLabel.title=v.name;row.append(dot,dotLabel,el('span','legend-size',fmt(v.size)),el('span','legend-percent',`${Math.round(v.size/total*100)}%`));legend.append(row)});
 const bar=$('barChart');bar.replaceChildren();
 if(total===0){bar.append(el('div','no-chart','暂无压缩统计'));}
 else parts.forEach((v,i)=>{const row=el('div','bar-row');row.append(el('span','',v.name.startsWith('其余')?'其余':`${i+1} 卷`));const track=el('div','bar-track'),fill=el('div','bar-fill');fill.style.width=`${(v.size/Math.max(...parts.map(x=>x.size)))*100}%`;fill.style.background=COLORS[i%COLORS.length];track.append(fill);row.append(track);bar.append(row)});
 $('donut').hidden=chartMode!=='donut';$('barChart').hidden=chartMode!=='bar';
}
function updateState(s,force){
 state=s;
 const inputs=s.inputs||[];
 updateFiles(inputs);
 if(initial){$('outputDir').value=s.outputDir||'';$('archiveName').value=s.archiveName||'我的压缩包';initial=false;}
 else if(force){if(s.outputDir)$('outputDir').value=s.outputDir;if(s.archiveName&&inputs.length)$('archiveName').value=s.archiveName;}
 $('originalSize').textContent=fmt(s.originalSize);$('originalDetail').textContent=s.originalSize?`（${Number(s.originalSize).toLocaleString('zh-CN')} 字节）`:'（等待添加文件）';
 $('compressedSize').textContent=fmt(s.totalSize);$('compressedDetail').textContent=s.volumes.length?`（${s.volumes.length} 个分卷）`:'（尚未开始压缩）';
 $('volumeCount').textContent=s.volumes.length;$('volumeStat').textContent=`${Number($('volumeSize').value)||512} MiB`;$('formatStat').textContent=$('format').value==='targz'?'tar.gz':'7z';
 $('progressPercent').textContent=`${s.percent||0}%`;$('progressFill').style.width=`${Math.min(100,Math.max(0,s.percent||0))}%`;
 const status=$('progressStatus');status.className='progress-status'+(s.successful?' success':s.finished?' failed':'');status.replaceChildren();
 const dot=el('span','status-dot '+(s.successful?'done':s.finished?'error': 'waiting'));dot.append(icon(s.successful?'check':s.finished?'close':'clock'));
 status.append(dot,document.createTextNode(' '+s.status));
 if(s.successful&&s.volumes.length)status.append(document.createTextNode(`（${fmt(s.totalSize)}）`));
 $('duration').textContent=`耗时 ${duration(s.elapsedSeconds)}`;
 renderCharts(s.volumes||[]);
 const signature=JSON.stringify(s.logs||[]);
 if(signature!==lastLogs){const log=$('logBody'),nearBottom=log.scrollHeight-log.scrollTop-log.clientHeight<40;log.replaceChildren();
  for(const line of s.logs||[]){const type=/完成|成功|通过/.test(line)?'good':/失败|错误/.test(line)?'bad':'',div=el('div','log-line '+type,line);log.append(div)}
  if(!s.logs?.length)log.append(el('div','log-line muted','欢迎使用 SplitVolume。添加文件或文件夹后即可开始。'));
  if(nearBottom)log.scrollTop=log.scrollHeight;
  lastLogs=signature;
 }
 document.querySelectorAll('input,select').forEach(x=>{
  if(['selectAll'].includes(x.id))return;
  if(x.closest('.files-table'))return;
  x.disabled=!!s.running;
 });
 for(const id of ['addFiles','addFolder','nativeDrop','clearItems','browseOutput','start'])$(id).disabled=!!s.running;
 $('cancel').disabled=!s.running;
 $('openOutput').disabled=!s.outputDir;
 $('selectAll').disabled=!!s.running||!inputs.length;
 syncChecks();
}
async function refresh(force=false){
 if(pollBusy)return;
 pollBusy=true;
 try{
  const s=await api('state',{},'GET');
  const recovered=connectionFailures>0;
  connectionFailures=0;
  updateState(s,force);
  if(recovered)toast('压缩服务已重新连接');
 }catch(e){
  connectionFailures++;
  if(connectionFailures===1||connectionFailures%20===0){
   toast('无法连接本地压缩服务：'+e.message+'。请确认 SplitVolume.exe 仍在运行；诊断日志位于 %LOCALAPPDATA%\\SplitVolume\\logs\\SplitVolume.log');
  }
 }finally{pollBusy=false}
}
$('addFiles').addEventListener('click',()=>action('add-files'));
$('addFolder').addEventListener('click',()=>action('add-folder'));
$('browseOutput').addEventListener('click',()=>action('browse-output'));
$('removeSelected').addEventListener('click',async()=>{const paths=[...selected];selected.clear();await action('remove',{paths});lastEntries='';if(state)updateFiles(state.inputs||[])});
$('clearItems').addEventListener('click',()=>{selected.clear();action('clear')});
$('clearLog').addEventListener('click',()=>action('clear-log'));
$('start').addEventListener('click',()=>{const p=currentParams();if(!p.volumeMiB||p.volumeMiB<1||p.volumeMiB>1048576)return toast('每卷大小必须在 1 至 1048576 MiB 之间');action('start',p)});
$('cancel').addEventListener('click',()=>action('cancel'));
$('openOutput').addEventListener('click',()=>action('open-output'));
$('selectAll').addEventListener('change',e=>{selected=new Set(e.target.checked?(state?.inputs||[]).map(x=>x.path):[]);lastEntries='';updateFiles(state?.inputs||[])});
for(const item of document.querySelectorAll('.chart-switch button'))item.addEventListener('click',()=>{chartMode=item.dataset.chart;document.querySelectorAll('.chart-switch button').forEach(b=>b.classList.toggle('selected',b===item));renderCharts(state?.volumes||[])});
$('volumeSize').addEventListener('input',()=>{$('volumeStat').textContent=`${Number($('volumeSize').value)||0} MiB`});
$('format').addEventListener('change',()=>{$('formatStat').textContent=$('format').value==='7z'?'7z':'tar.gz';if($('format').value==='7z'&&!state?.sevenZipAvailable)modal('7-Zip 引擎未找到','选择 7z 格式需要在 Windows 上安装 7-Zip。推荐保留 tar.gz 分卷格式，Linux 上无需 7z。')});
// Help is a separate full page; compression state keeps polling while it is open.
function routePage(){
 const help=location.hash==='#help';
 $('homePage').hidden=help;
 $('helpPage').hidden=!help;
 $('settingsHelp').setAttribute('aria-current',help?'page':'false');
 document.title=help?'解压帮助 · SplitVolume':'分卷压缩助手 · SplitVolume';
 
}
$('settingsHelp').addEventListener('click',()=>{hideOverlays();location.hash='help';routePage()});
$('backHome').addEventListener('click',()=>{location.hash='';routePage()});
window.addEventListener('hashchange',routePage);
// Keep the primary workspace small; show dense information only when requested.
const overlayIds=['statsOverlay','logsOverlay'];
function hideOverlays(){for(const id of overlayIds)$(id).hidden=true}
function showOverlay(id){hideOverlays();$(id).hidden=false}
for(const id of ['showStats','moreStats'])$(id).addEventListener('click',()=>showOverlay('statsOverlay'));
$('showLogs').addEventListener('click',()=>showOverlay('logsOverlay'));
$('closeStats').addEventListener('click',hideOverlays);
$('closeLogs').addEventListener('click',hideOverlays);
for(const id of overlayIds)$(id).addEventListener('click',e=>{if(e.target===$(id))hideOverlays()});
window.addEventListener('keydown',e=>{if(e.key==='Escape'){hideOverlays();$('modal').hidden=true}});
for(const tab of document.querySelectorAll('[data-help]'))tab.addEventListener('click',()=>{
 for(const btn of document.querySelectorAll('[data-help]'))btn.classList.toggle('active',btn===tab);
 for(const panel of document.querySelectorAll('[data-help-panel]'))panel.hidden=panel.dataset.helpPanel!==tab.dataset.help;
});
$('homePage').dataset.pane='files';
for(const tab of document.querySelectorAll('[data-pane]'))tab.addEventListener('click',()=>{
 $('homePage').dataset.pane=tab.dataset.pane;
 for(const btn of document.querySelectorAll('[data-pane]'))btn.classList.toggle('active',btn===tab);
});
for(const btn of document.querySelectorAll('[data-copy-target]'))btn.addEventListener('click',async()=>{
 const txt=$(btn.dataset.copyTarget)?.textContent||'';
 try{
  if(navigator.clipboard?.writeText){await navigator.clipboard.writeText(txt)}
  else{const input=document.createElement('textarea');input.value=txt;input.style.position='fixed';input.style.opacity='0';document.body.append(input);input.select();if(!document.execCommand('copy'))throw new Error('复制失败');input.remove()}
  const label=btn.lastChild;label.textContent=' 已复制';setTimeout(()=>label.textContent=' 复制',1400);
 }catch(e){toast('无法访问剪贴板，请手动选中代码复制')}
});
routePage();
$('about').addEventListener('click',()=>modal('关于 SplitVolume','SplitVolume · 分卷压缩助手\n\n紧凑版界面、分卷实时统计、运行日志与 Linux 解压帮助。\n内置 Go tar.gz 流式压缩引擎。界面使用隔离的浏览器应用配置，禁用扩展、离线工作。'));
window.addEventListener('beforeunload',e=>{if(state?.running){e.preventDefault();e.returnValue='压缩任务正在运行，关闭窗口不会中断后台任务。'}});
// A browser's DataTransfer API does not expose source absolute paths.
// Never upload or duplicate dragged file contents; use the native Win32 drop
// target (WM_DROPFILES) to receive the original path from Windows Explorer.
let dropRequestPending=false;
async function launchNativeDrop(){
 if(state?.running){toast('压缩期间无法添加文件');return}
 if(dropRequestPending)return;
 dropRequestPending=true;
 try{await api('native-drop');toast('请将文件或文件夹拖入弹出的原生窗口；直接读取原始路径，不复制文件')}
 catch(e){toast(e.message)}
 finally{setTimeout(()=>dropRequestPending=false,1200)}
}
$('nativeDrop').addEventListener('click',launchNativeDrop);
window.addEventListener('dragenter',e=>{
 if(!Array.from(e.dataTransfer?.types||[]).includes('Files'))return;
 e.preventDefault();document.body.classList.add('dragging');
 launchNativeDrop(); // Open a real shell drop target during the drag gesture.
});
window.addEventListener('dragover',e=>{
 if(!Array.from(e.dataTransfer?.types||[]).includes('Files'))return;
 e.preventDefault();if(e.dataTransfer)e.dataTransfer.dropEffect='none';
});
window.addEventListener('dragleave',e=>{if(!e.relatedTarget)document.body.classList.remove('dragging')});
window.addEventListener('drop',e=>{
 e.preventDefault();document.body.classList.remove('dragging');
 // Edge's page cannot provide a trustworthy source pathname. The native
 // drop zone may already be open from dragenter; never stream bytes instead.
 launchNativeDrop();
});
// Do not stop the Go backend merely because this page stops polling: the OS
// can suspend a window for minutes. Best-effort close hint; a reload cancels it.
window.addEventListener('pagehide',()=>{
 try{fetch('/api/window-closed',{method:'POST',headers:{'X-Split-Token':token,'Content-Type':'application/json'},body:'{}',keepalive:true})}catch(_){}
});
refresh(true);setInterval(()=>refresh(false),850);
})();
