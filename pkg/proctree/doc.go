// Package proctree owns process-group lifecycle for subprocess trees:
// make the child a group leader, then signal the whole group on
// terminate/kill so wrappers cannot orphan the process doing the real
// work. Extracted from pkg/plugins (ADR-013 fixed the orphan bug there;
// ADR-029's code-node workers need the identical contract), which now
// delegates here.
package proctree
