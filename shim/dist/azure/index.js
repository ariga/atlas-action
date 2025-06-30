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

const childProcess = __nccwpck_require__(81);
const fs = __nccwpck_require__(147);
const path = __nccwpck_require__(17);

// The action input uses spaces (e.g., "schema plan approve") instead of slashes
// due to limitations in Azure DevOps task.json's visibleRule field,
// which does not handle '/' or quoted strings well.
//
// We convert the space-separated action string to the slash-separated
// format expected by the atlas-action binary (e.g., "schema/plan/approve").
const action = (process.env.INPUT_ACTION || "").trim().replaceAll(" ", "/").toLowerCase();
if (!action) {
  throw new Error("Missing required input: action.");
}
const bin = path.join(__dirname, "atlas-action");
try {
  // Only change permission if execute is not set
  const stat = fs.statSync(bin);
  if ((stat.mode & 0o111) === 0) {
    fs.chmodSync(bin, stat.mode | 0o111);
  }
} catch (err) {
  console.error("##[error]OS currently is not supported.");
  process.exit(1);
}
const { status, error } = childProcess.spawnSync(bin, ["--action", action], {
  stdio: "inherit"
});
if (status !== 0 || error) {
  if (error) {
    console.log("##[error]" + error);
  }
  // Always exit with an error code to fail the action
  process.exit(status || 1);
}

})();

module.exports = __webpack_exports__;
/******/ })()
;