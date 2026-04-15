/******/ (() => { // webpackBootstrap
/******/ 	var __webpack_modules__ = ({

/***/ 81:
/***/ ((module) => {

"use strict";
module.exports = require("child_process");

/***/ }),

/***/ 147:
/***/ ((module) => {

"use strict";
module.exports = require("fs");

/***/ }),

/***/ 37:
/***/ ((module) => {

"use strict";
module.exports = require("os");

/***/ }),

/***/ 17:
/***/ ((module) => {

"use strict";
module.exports = require("path");

/***/ })

/******/ 	});
/************************************************************************/
/******/ 	// The module cache
/******/ 	var __webpack_module_cache__ = {};
/******/ 	
/******/ 	// The require function
/******/ 	function __nccwpck_require__(moduleId) {
/******/ 		// Check if module is in cache
/******/ 		var cachedModule = __webpack_module_cache__[moduleId];
/******/ 		if (cachedModule !== undefined) {
/******/ 			return cachedModule.exports;
/******/ 		}
/******/ 		// Create a new module (and put it into the cache)
/******/ 		var module = __webpack_module_cache__[moduleId] = {
/******/ 			// no module.id needed
/******/ 			// no module.loaded needed
/******/ 			exports: {}
/******/ 		};
/******/ 	
/******/ 		// Execute the module function
/******/ 		var threw = true;
/******/ 		try {
/******/ 			__webpack_modules__[moduleId](module, module.exports, __nccwpck_require__);
/******/ 			threw = false;
/******/ 		} finally {
/******/ 			if(threw) delete __webpack_module_cache__[moduleId];
/******/ 		}
/******/ 	
/******/ 		// Return the exports of the module
/******/ 		return module.exports;
/******/ 	}
/******/ 	
/************************************************************************/
/******/ 	/* webpack/runtime/compat */
/******/ 	
/******/ 	if (typeof __nccwpck_require__ !== 'undefined') __nccwpck_require__.ab = __dirname + "/";
/******/ 	
/************************************************************************/
var __webpack_exports__ = {};
// This entry need to be wrapped in an IIFE because it need to be isolated against other modules in the chunk.
(() => {
// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

// postjobexecution hook for AtlasAction.
// Only runs when action == "setup". Copies ~/.atlas back into
// $(Pipeline.Workspace)/.atlas so that a Cache@2 step with
// path: $(Pipeline.Workspace)/.atlas can persist the grant.

const childProcess = __nccwpck_require__(81);
const fs = __nccwpck_require__(147);
const os = __nccwpck_require__(37);
const path = __nccwpck_require__(17);

const action = (process.env.INPUT_ACTION || "").trim().replaceAll(" ", "/").toLowerCase();
if (action !== "setup") {
  process.exit(0);
}

const pipelineWorkspace = process.env.PIPELINE_WORKSPACE || process.env.AGENT_BUILDDIRECTORY;
if (!pipelineWorkspace) {
  console.log("post-shim: PIPELINE_WORKSPACE and AGENT_BUILDDIRECTORY are both unset — skipping cache copy.");
  process.exit(0);
}

const cacheDir = path.join(pipelineWorkspace, ".atlas");
const homeAtlas = path.join(os.homedir(), ".atlas");

if (!fs.existsSync(homeAtlas)) {
  console.log("post-shim: ~/.atlas does not exist — nothing to save.");
  process.exit(0);
}

fs.mkdirSync(cacheDir, { recursive: true });
console.log(`post-shim: copying ${homeAtlas} → ${cacheDir}`);
const { status } = childProcess.spawnSync("cp", ["-a", homeAtlas + "/.", cacheDir + "/"], { stdio: "inherit" });
if (status === 0) {
  console.log("post-shim: grant cache saved successfully.");
} else {
  console.log(`post-shim: cp exited with status ${status}.`);
}
process.exit(status || 0);

})();

module.exports = __webpack_exports__;
/******/ })()
;