import {ModalBackdrop, useDialogGuard} from "./Modal";
import {useEffect,useRef,useState,type ReactNode} from 'react';
import './applicationSettings.css';
export function SettingsDialog({close,children}:{close:()=>void;children:ReactNode}) {
 return <ModalBackdrop className="modal-backdrop application-settings-backdrop" close={close}><section className="application-settings" role="dialog" aria-modal="true" aria-label="设置">{children}</section></ModalBackdrop>;
}

export function SettingsPage({title,description,actions,children,className=''}:{title:string;description:string;actions?:ReactNode;children:ReactNode;className?:string}) {
 return <section className={`settings-page ${className}`}><header className="settings-page-header"><div><h2>{title}</h2><p>{description}</p></div>{actions&&<div className="settings-page-actions">{actions}</div>}</header><div className="settings-page-body">{children}</div></section>
}

type Service=<T>(facade:string,method:string,...args:unknown[])=>Promise<T>;
type Proxy={mode:'inherit'|'direct'|'custom';url:string};
type Config={global:Proxy;modules:Record<string,Proxy>;revision:number};
const labels:Record<string,string>={model:'模型 API 与向量模型',search:'联网搜索与网页读取',research:'文献检索与全文下载',dependencies:'Python 与依赖安装',mcp:'MCP 服务',skills:'Skill 下载',browser:'浏览器访问'};
const blank:Proxy={mode:'inherit',url:''};
export function SearchNetworkStatus({service,openNetwork}:{service:Service;openNetwork:()=>void}) {
 const [config,setConfig]=useState<Config|null>(null),[error,setError]=useState(false);
 useEffect(()=>{let active=true;service<Config>('ModelFacade','GetNetworkConfig').then(v=>{if(active)setConfig(v)}).catch(()=>{if(active)setError(true)});return()=>{active=false}},[service]);
 const local=config?.modules.search;
 const effective=local&&local.mode!=='inherit'?local:config?.global;
 const label=error?'网络状态读取失败':!config?'正在读取网络设置…':effective?.mode!=='custom'?'直连':local?.mode==='custom'?'搜索独立代理':'全局代理';
 return <section className="search-network-status"><span><i aria-hidden="true"/>{label}</span><button type="button" onClick={openNetwork}>前往网络设置 <span aria-hidden="true">↗</span></button></section>
}
export function NetworkSettings({service}:{service:Service}) {
 const [config,setConfig]=useState<Config|null>(null),[busy,setBusy]=useState(false),[message,setMessage]=useState('');
 const saved=useRef('');
 useDialogGuard({busy,dirty:!!config && !!saved.current && JSON.stringify(config)!==saved.current});
 useEffect(()=>{let active=true;service<Config>('ModelFacade','GetNetworkConfig').then(v=>{if(active){saved.current=JSON.stringify(v);setConfig(v)}}).catch(e=>{if(active)setMessage(String(e))});return()=>{active=false}},[service]);
 function editor(key:string,label:string) {
  if(!config)return null;
  const value=key==='global'?config.global:config.modules[key]??blank;
  const update=(p:Proxy)=>{setMessage('');setConfig(key==='global'?{...config,global:p}:{...config,modules:{...config.modules,[key]:p}})};
  return <section className={`network-row ${value.mode==='custom'?'is-custom':''}`} key={key}>
   <b>{label}</b>
   <div className={`network-select-wrap mode-${value.mode}`}>
    <select aria-label={`${label}模式`} disabled={busy} value={value.mode} onChange={e=>update({...value,mode:e.target.value as Proxy['mode']})}>
     {key!=='global'&&<option value="inherit">继承全局</option>}<option value="direct">直连</option><option value="custom">自定义代理</option>
    </select><svg aria-hidden="true" width="14" height="14" viewBox="0 0 16 16"><path d="m4 6 4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"/></svg>
   </div>
   {value.mode==='custom'&&<input aria-label={`${label}地址`} value={value.url} disabled={busy} placeholder="http://127.0.0.1:7890" spellCheck={false} onChange={e=>update({...value,url:e.target.value})}/>}
  </section>
 }
 async function save(){if(!config)return;setBusy(true);setMessage('');try{await service('ModelFacade','SaveNetworkConfig',config);const updated=await service<Config>('ModelFacade','GetNetworkConfig');saved.current=JSON.stringify(updated);setConfig(updated);setMessage('已保存，新请求生效')}catch(e){setMessage(String(e))}finally{setBusy(false)}}
 return <SettingsPage title="网络与代理" description="统一管理应用网络出口，按需覆盖全局配置。" className="network-settings-page"><div className="network-settings">
  {config?<>
   <div className="network-global">{editor('global','全局网络')}</div>
   <div className="network-section-title"><h3>独立设置</h3><span>按需覆盖全局配置</span></div>
   <div className="network-modules">{Object.entries(labels).map(([key,label])=>editor(key,label))}</div>
   <details className="network-notes"><summary>连接说明</summary><p>支持 HTTP / HTTPS / SOCKS5，暂不支持账号密码。代理失败不会回退直连，本机通信绕过代理。已运行的 Python Kernel 和 MCP 子进程需重启后采用新设置；第三方程序若忽略代理环境变量，其网络不保证被接管。Cookie 复用需要固定出口 IP，轮换代理不适合。</p></details>
   <footer><span role="status">{message}</span><button type="button" disabled={busy} onClick={()=>void save()}>{busy?'保存中…':'保存设置'}</button></footer>
  </>:<p role="status">{message||'正在加载…'}</p>}
 </div></SettingsPage>
}
type BrowserStatus={ready:boolean;root:string;binaryPath:string;message:string};
export function BrowserEnvironment({service,projectId,enabled,onChanged}:{service:Service;projectId:string;enabled:boolean;onChanged:()=>void}){
 const [status,setStatus]=useState<BrowserStatus|null>(null),[busy,setBusy]=useState(false),[open,setOpen]=useState(false),[message,setMessage]=useState('');
 useDialogGuard({busy});
 useEffect(()=>{if(open)void service<BrowserStatus>('PythonFacade','GetBrowserEnvironment',projectId).then(setStatus).catch(e=>setMessage(String(e)))},[open,projectId]);
 async function install(){setBusy(true);setMessage('正在安装项目依赖与浏览器，首次下载可能较大…');try{const v=await service<BrowserStatus>('PythonFacade','InstallBrowserEnvironment',projectId);setStatus(v);onChanged();setMessage('配置完成；浏览器工具可供新请求使用。')}catch(e){setMessage(String(e));onChanged()}finally{setBusy(false)}}
 return <section className="browser-environment"><button type="button" onClick={()=>setOpen(v=>!v)}>配置浏览器环境 <span>{open?'−':'＋'}</span></button>{open&&<div><p>仅安装到当前项目 Python 环境；Chrome、GeoIP 与缓存保存至项目 <code>.sciaide/browser</code>，不会使用个人浏览器会话。依赖下载继承“依赖安装”代理，文献验证沿用该下载请求的代理。</p><b>{status?.ready?'浏览器可用':status?.message||'尚未配置'}</b>{status?.root&&<code>{status.root}</code>}<p role="status">{message}</p><div className="browser-env-actions"><button type="button" disabled={busy||!enabled} onClick={()=>void install()}>{busy?'配置中…':status?.ready?'检查并修复':'安装浏览器环境'}</button>{busy&&<button type="button" onClick={()=>void service('PythonFacade','CancelBrowserOperation',projectId).catch(e=>setMessage(String(e)))}>取消安装</button>}</div>{!enabled&&<small>请先创建当前项目的 Workspace 托管 Python 环境；不会修改外部环境。</small>}</div>}</section>
}
