package page

import (
	"context"
	"fmt"
	"io"

	"github.com/a-h/templ"
)

// cfgSystemSection renders the Entity configuration tab content.
// Written as a Go component to avoid re-generating configuration_templ.go.
func cfgSystemSection(entityName string, systemWindowDays int) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, `<div style="display:grid;grid-template-columns:1fr 1fr;gap:20px;align-items:start">
<div style="border:1px solid var(--border);border-radius:6px;padding:16px 18px;background:var(--surface)">
<h2 style="font-size:13px;font-weight:600;color:var(--text);margin:0 0 4px">Entity name</h2>
<p style="font-size:12px;color:var(--text3);margin:0 0 12px">Display name for this entity.</p>
<div style="display:flex;align-items:center;gap:8px">
<input id="entity-name-input" type="text" value="%s" style="flex:1;min-width:0;background:var(--bg);border:1px solid var(--border);border-radius:5px;padding:6px 10px;font-size:13px;color:var(--text);font-family:inherit"/>
<button id="entity-name-save" style="background:var(--accent);border:none;border-radius:5px;padding:6px 14px;cursor:pointer;font-size:13px;font-weight:500;color:#fff;font-family:inherit;flex-shrink:0">Save</button>
</div>
<span id="entity-name-msg" style="display:block;font-size:12px;color:var(--text3);margin-top:6px;min-height:1em"></span>
</div>
<div style="border:1px solid var(--border);border-radius:6px;padding:16px 18px;background:var(--surface)">
<h2 style="font-size:13px;font-weight:600;color:var(--text);margin:0 0 4px">System rate window</h2>
<p style="font-size:12px;color:var(--text3);margin:0 0 12px">Days used to compute the rolling-window rate for the Income and Spend system entries.</p>
<div style="display:flex;align-items:center;gap:8px">
<input id="system-window-input" type="number" min="1" max="365" value="%d" style="width:90px;background:var(--bg);border:1px solid var(--border);border-radius:5px;padding:6px 10px;font-size:13px;color:var(--text);font-family:inherit"/>
<button id="system-window-save" style="background:var(--accent);border:none;border-radius:5px;padding:6px 14px;cursor:pointer;font-size:13px;font-weight:500;color:#fff;font-family:inherit;flex-shrink:0">Save</button>
</div>
<span id="system-window-msg" style="display:block;font-size:12px;color:var(--text3);margin-top:6px;min-height:1em"></span>
</div>
</div>
<script>
(function(){
var nameInp=document.getElementById('entity-name-input');
var nameBtn=document.getElementById('entity-name-save');
var nameMsg=document.getElementById('entity-name-msg');
if(nameBtn){
nameBtn.addEventListener('click',function(){
var v=(nameInp.value||'').trim();
if(!v){nameMsg.textContent='Name is required.';return;}
nameBtn.disabled=true;nameMsg.textContent='';
fetch('/api/entity/name',{method:'PUT',headers:{'Content-Type':'application/json'},credentials:'same-origin',body:JSON.stringify({name:v})})
.then(function(r){nameBtn.disabled=false;if(r.ok){nameMsg.textContent='Saved.';}else{nameMsg.textContent='Failed to save.';}})
.catch(function(){nameBtn.disabled=false;nameMsg.textContent='Failed to save.';});
});
}
var inp=document.getElementById('system-window-input');
var btn=document.getElementById('system-window-save');
var msg=document.getElementById('system-window-msg');
if(!btn)return;
btn.addEventListener('click',function(){
var v=parseInt(inp.value,10);
if(!v||v<1||v>365){msg.textContent='Enter a value between 1 and 365.';return;}
btn.disabled=true;msg.textContent='';
fetch('/api/entity/config',{method:'PUT',headers:{'Content-Type':'application/json'},credentials:'same-origin',body:JSON.stringify({system_window_days:v})})
.then(function(r){btn.disabled=false;if(r.ok){msg.textContent='Saved.';}else{msg.textContent='Failed to save.';}})
.catch(function(){btn.disabled=false;msg.textContent='Failed to save.';});
});
})();
</script>`, entityName, systemWindowDays)
		return err
	})
}
