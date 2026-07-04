import {CsEnv, getCsEnv, setCsEnv, Templater} from './env.js';

function runSpec(): void {
    const env = getCsEnv();
    const x = CsEnv.from("dev");
    const y = CsEnv.empty();
    setCsEnv(CsEnv.from("prod"));
    const t = new Templater("{{a}}");
    t.render();
}
