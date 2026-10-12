(async () => {
  const hash = async (value) => {
    const bytes = typeof value === 'string' ? new TextEncoder().encode(value) : new Uint8Array(value.buffer || value);
    return Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))).map(x => x.toString(16).padStart(2,'0')).join('');
  };
  const navigatorFields = async () => ({
    userAgent:navigator.userAgent, platform:navigator.platform,
    language:navigator.language, languages:Array.from(navigator.languages),
    hardwareConcurrency:navigator.hardwareConcurrency, deviceMemory:navigator.deviceMemory,
    webdriver:navigator.webdriver,
    uaData:navigator.userAgentData ? await navigator.userAgentData.getHighEntropyValues(['architecture','bitness','model','platformVersion','uaFullVersion','fullVersionList']) : null,
    timezone:Intl.DateTimeFormat().resolvedOptions().timeZone,
  });
  const canvasSignals = async (canvas) => {
    const c=canvas.getContext('2d');
    c.fillStyle='#173451'; c.fillRect(0,0,240,80);
    c.fillStyle='#ab72ce'; c.font='19px Arial'; c.fillText('Account fixture 012345',7,27);
    c.globalCompositeOperation='multiply'; c.fillStyle='rgba(205,51,173,.72)'; c.beginPath(); c.arc(90,40,27,0,Math.PI*2);c.fill();
    const image=await hash(c.getImageData(0,0,240,80).data);
    let encoded;
    if(canvas.toDataURL) encoded=await hash(canvas.toDataURL());
    else encoded=await hash(await (await canvas.convertToBlob()).arrayBuffer());
    return {image,encoded,textMetrics:c.measureText('Account fixture').width};
  };
  const webglSignals = async (canvas) => {
    const gl=canvas.getContext('webgl'); if(!gl)return {supported:false};
    const debug=gl.getExtension('WEBGL_debug_renderer_info');
    const compile=(type,source)=>{const s=gl.createShader(type);gl.shaderSource(s,source);gl.compileShader(s);return s;};
    const p=gl.createProgram(); gl.attachShader(p,compile(gl.VERTEX_SHADER,'attribute vec2 p;void main(){gl_Position=vec4(p,0.,1.);}'));
    gl.attachShader(p,compile(gl.FRAGMENT_SHADER,'precision mediump float;void main(){gl_FragColor=vec4(gl_FragCoord.x/240.,gl_FragCoord.y/80.,0.3,1.);}'));gl.linkProgram(p);gl.useProgram(p);
    const b=gl.createBuffer();gl.bindBuffer(gl.ARRAY_BUFFER,b);gl.bufferData(gl.ARRAY_BUFFER,new Float32Array([-1,-1,1,-1,0,1]),gl.STATIC_DRAW);
    const a=gl.getAttribLocation(p,'p');gl.enableVertexAttribArray(a);gl.vertexAttribPointer(a,2,gl.FLOAT,false,0,0);gl.viewport(0,0,240,80);gl.drawArrays(gl.TRIANGLES,0,3);
    const pixels=new Uint8Array(240*80*4);gl.readPixels(0,0,240,80,gl.RGBA,gl.UNSIGNED_BYTE,pixels);
    const repeated=new Uint8Array(240*80*4);gl.readPixels(0,0,240,80,gl.RGBA,gl.UNSIGNED_BYTE,repeated);
    let changedChannels=0,changedPixels=0,maxDelta=0,totalDelta=0;for(let i=0;i<pixels.length;i+=4){let changed=false;for(let j=0;j<4;j++){const delta=Math.abs(pixels[i+j]-repeated[i+j]);if(delta){changed=true;changedChannels++;totalDelta+=delta;maxDelta=Math.max(maxDelta,delta);}}if(changed)changedPixels++;}
    return {supported:true,vendor:gl.getParameter(gl.VENDOR),renderer:gl.getParameter(gl.RENDERER),unmaskedVendor:debug?gl.getParameter(debug.UNMASKED_VENDOR_WEBGL):null,unmaskedRenderer:debug?gl.getParameter(debug.UNMASKED_RENDERER_WEBGL):null,version:gl.getParameter(gl.VERSION),maxTextureSize:gl.getParameter(gl.MAX_TEXTURE_SIZE),maxRenderbufferSize:gl.getParameter(gl.MAX_RENDERBUFFER_SIZE),extensions:gl.getSupportedExtensions().sort(),pixels:await hash(pixels),pixelsRepeated:await hash(repeated),repeatDifference:{changedChannels,changedPixels,maxDelta,totalDelta,totalPixels:240*80},pixelSample:Array.from(pixels.slice(40000,40064)),error:gl.getError()};
  };
  const makeCanvas=()=>{const c=document.createElement('canvas');c.width=240;c.height=80;return c;};
  const page=await navigatorFields();
  page.canvas=await canvasSignals(makeCanvas()); page.webgl=await webglSignals(makeCanvas());
  page.screen={width:screen.width,height:screen.height,availWidth:screen.availWidth,availHeight:screen.availHeight,colorDepth:screen.colorDepth,pixelDepth:screen.pixelDepth,dpr:devicePixelRatio,innerWidth,innerHeight};
  page.plugins=Array.from(navigator.plugins).map(x=>x.name);
  const rect=document.createElement('div');rect.style.cssText='position:absolute;left:10.25px;top:12.75px;width:123.5px;height:35.25px;font:19px Arial';rect.textContent='geometry fixture';document.body.append(rect);page.rect=JSON.parse(JSON.stringify(rect.getBoundingClientRect()));page.clientRects=Array.from(rect.getClientRects()).map(x=>JSON.parse(JSON.stringify(x)));rect.remove();
  const audio=new OfflineAudioContext(1,4096,44100);const oscillator=audio.createOscillator();oscillator.type='triangle';oscillator.frequency.value=10000;const compressor=audio.createDynamicsCompressor();oscillator.connect(compressor);compressor.connect(audio.destination);oscillator.start();const rendered=await audio.startRendering();page.audio={contextSampleRate:audio.sampleRate,bufferSampleRate:rendered.sampleRate,samples:await hash(rendered.getChannelData(0))};
  page.fonts={arial:document.fonts.check('19px Arial'),dejavu:document.fonts.check('19px "DejaVu Sans"'),noto:document.fonts.check('19px "Noto Sans"')};
  const workerSource='const hash='+hash.toString()+';const navigatorFields='+navigatorFields.toString()+';const canvasSignals='+canvasSignals.toString()+';const webglSignals='+webglSignals.toString()+';onmessage=async()=>{try{const r=await navigatorFields();r.canvas=await canvasSignals(new OffscreenCanvas(240,80));r.webgl=await webglSignals(new OffscreenCanvas(240,80));postMessage(r);}catch(e){postMessage({error:String(e)});}}';
  const worker=await new Promise((resolve,reject)=>{const url=URL.createObjectURL(new Blob([workerSource],{type:'application/javascript'}));const w=new Worker(url);const timer=setTimeout(()=>{w.terminate();reject(new Error('worker timeout'));},30000);w.onmessage=e=>{clearTimeout(timer);w.terminate();URL.revokeObjectURL(url);resolve(e.data);};w.onerror=e=>{clearTimeout(timer);w.terminate();reject(new Error(e.message));};w.postMessage(1);});
  return {page,worker};
})()
