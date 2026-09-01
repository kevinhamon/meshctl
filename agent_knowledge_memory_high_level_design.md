# Agent-Specific Knowledge and Memory Retrieval

## High-Level Design

*Iteration-oriented design brief*

## 1. Purpose

Define a high-level model for agent-specific knowledge and memory that can be introduced as an iteration on the current agent system. The design preserves a durable, human-readable knowledge base while adding a complementary memory layer that helps an agent recognize prior familiarity and route itself to authoritative knowledge when deeper recall is required.

## 2. Scope

- Define the conceptual roles of the agent knowledge base, memory, and working context.
- Define the recommended information structure of an agent-owned knowledge base.
- Define the memory and knowledge retrieval behavior expected of the agent.
- Define high-level lifecycle, governance, and recovery expectations.

## 3. Non-Goals

- Select a database, vector store, framework, MCP implementation, embedding model, or agent runtime.
- Prescribe file formats beyond the logical content structure.
- Redesign the current agent platform or replace existing knowledge assets.
- Specify APIs, schemas, synchronization mechanisms, indexing pipelines, or deployment architecture.

## 4. Conceptual Model

| Layer | Primary Role | Authority | Expected Lifetime |
|---|---|---|---|
| Knowledge Base | Maintains detailed understanding, procedures, rationale, and durable agent-specific domain knowledge. | Authoritative for maintained agent knowledge. | Durable and recoverable. |
| Memory | Signals that the agent has encountered a topic and identifies where deeper knowledge may be found. | Advisory and non-authoritative. | Disposable and rebuildable. |
| Working Context | Holds information required to perform the current task. | Task-local. | Temporary. |
| Live Systems | Provide current operational facts and system-of-record evidence. | Authoritative for current state. | External and independently maintained. |

The intended flow is:

> Memory signals relevance; the knowledge base restores detailed understanding; live systems verify current reality; the agent acts.

## 5. Agent Knowledge Base Structure

Each agent should have an isolated knowledge domain organized by the cognitive purpose of the content, rather than as an undifferentiated collection of notes.

| Knowledge Area | Purpose | Typical Content |
|---|---|---|
| Agent Contract | Defines stable role, responsibilities, operating principles, boundaries, and escalation expectations. | Mission, authority limits, required behaviors, tool-use policy. |
| Knowledge | Maintains the agent's durable domain and organizational model. | Terminology, product model, operating model, stakeholder model, system relationships. |
| Decisions | Preserves decisions and the reasoning that shaped the agent's current understanding. | Context, decision, rationale, consequences, superseded guidance. |
| Playbooks | Defines repeatable procedures and decision frameworks. | Intake, analysis, planning, decomposition, escalation, review procedures. |
| Experience | Captures reusable lessons derived from prior work. | Successful patterns, failed patterns, retrospectives, recurring failure modes. |
| State | Represents the agent's current maintained working model. | Current priorities, active initiatives, unresolved conflicts, open questions. |
| Inbox | Separates proposed learning from accepted knowledge. | Candidate updates, tentative observations, material awaiting review or consolidation. |

## 6. Knowledge Characteristics

- Knowledge should be coherent and maintained, not merely accumulated.
- Detailed knowledge should preserve context, rationale, scope, exceptions, and relationships.
- Current guidance should be distinguishable from historical or superseded guidance.
- Tentative observations should remain separate from accepted agent knowledge.
- The knowledge base should be sufficient for the agent to restudy and reconstruct its operating understanding after memory loss.

## 7. Memory Model

Memory is a recognition and routing layer. It should help the agent determine whether it already has relevant knowledge and where that knowledge can be recovered. It should not become a competing source of truth.

| Memory Type | Purpose | Example Signal |
|---|---|---|
| Topic Familiarity | Indicates that the agent has prior knowledge about a subject. | Established guidance exists for cross-team initiative planning. |
| Location Memory | Points toward likely knowledge locations or authoritative systems. | Relevant material exists in the roadmap playbook and prior initiative decisions. |
| Relationship Memory | Reminds the agent that entities or concepts are connected. | This product area depends on a separate platform and owner. |
| Experience Marker | Signals that a prior lesson or recurring pattern should be reviewed. | Previous initiatives of this type failed when dependency ownership was implicit. |

## 8. Memory Content Boundaries

- Memory may contain a compact orientation, familiarity strength, freshness, and references to deeper knowledge.
- Memory should avoid duplicating complete procedures, decision rationale, or detailed domain models.
- A memory without supporting knowledge references should be treated as tentative.
- A material decision should not rely on a compressed memory when deeper knowledge or current evidence is available.

## 9. Memory and Knowledge Retrieval Specification

When a task is received, the agent should use memory to recognize relevant prior understanding and to determine whether deeper retrieval is warranted.

| Stage | Expected Behavior |
|---|---|
| 1. Recognize | Identify topics, entities, relationships, and task patterns that may correspond to prior knowledge. |
| 2. Recall | Consult agent memory for familiarity indicators, likely knowledge locations, related concepts, and freshness signals. |
| 3. Retrieve | Read the referenced knowledge-base material needed to restore detailed understanding. |
| 4. Reconcile | Resolve conflicts between memory and the knowledge base in favor of the maintained knowledge base. |
| 5. Verify | Confirm time-sensitive or operational facts against current systems of record. |
| 6. Act | Perform the task using restored knowledge and verified current context. |
| 7. Learn | Identify whether the task produced a durable lesson, state change, or candidate knowledge update. |

## 10. Knowledge Update and Governance

- The agent may maintain temporary working state as part of normal operation.
- New durable knowledge should first be identified as a proposed learning or candidate update.
- Accepted knowledge should be consolidated into the appropriate knowledge area rather than appended indefinitely.
- Changes to stable behavior, operating principles, or authority boundaries require stronger review than ordinary domain learning.
- Superseded knowledge should remain traceable but clearly inactive.

## 11. Recovery and Rebuild Expectations

- Loss or replacement of the memory store must not result in loss of authoritative agent knowledge.
- The agent should be capable of restudying its knowledge base and rebuilding useful familiarity indicators.
- Memory quality may improve performance and continuity, but the agent's core competence must remain recoverable from the knowledge base.
- The knowledge base must remain usable independently of any specific memory implementation.

## 12. Design Constraints

- The design must fit the current system incrementally.
- Existing agent knowledge assets should be preserved and reorganized only where useful.
- Memory and knowledge must remain logically separable.
- The design should support agent isolation while permitting deliberate references to shared organizational knowledge.
- The selected implementation should avoid making the memory layer a hidden or irreplaceable source of truth.

## 13. Success Criteria

- The agent can recognize that it has prior knowledge about a topic without loading the entire knowledge base.
- The agent can reliably locate and restudy the detailed knowledge needed for a task.
- The agent distinguishes remembered indicators from authoritative knowledge and current system facts.
- The agent can recover useful operation after memory loss by restudying its knowledge base.
