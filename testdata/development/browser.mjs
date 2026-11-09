import {pathToFileURL} from 'node:url';
const {chromium}=await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE));
for(const viewport of [{width:1280,height:800},{width:390,height:844}]){
 const browser=await chromium.launch({headless:true,executablePath:process.env.RUNNER_CHROMIUM,args:['--disable-gpu','--single-process','--no-zygote']});
 try {
  const page=await browser.newPage({viewport});
  await page.goto(process.argv[2]+'/?name=%3Cscript%3E');
  if(await page.locator('h1').innerText()!=='Hello, <script>!')throw Error('wrong greeting');
  if(await page.locator('script').count()!==0)throw Error('name interpreted as markup');
 }finally{await browser.close()}
}
console.log('Headless desktop/mobile journeys passed against the retained executable.');
