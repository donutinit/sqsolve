// SPDX-License-Identifier: AGPL-3.0-only

// Package drp builds DaVinci Resolve project files (.drp) and grade files
// (.drx) from a project library, reading it only. It is the experimental
// exception to the black-box principle (ADR 0019, docs 22).
//
// A .drp is a zip of XML documents and a .drx is one XML document; both
// serialize rows of the library with one element per object class and one
// child element per field, in the order Resolve itself uses. The serialization
// model (classes, fields, kinds, association tables) lives in model.json and
// was learned from files exported by Resolve; everything that was not seen in
// those files makes the export refuse instead of guessing.
//
// Everything here runs inside a READ ONLY transaction with a statement
// timeout. Table and column names come from the embedded model and are still
// quoted with pgx.Identifier.Sanitize; values never reach SQL text.
package drp
