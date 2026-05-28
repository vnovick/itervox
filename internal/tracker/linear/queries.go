package linear

// noProjectSentinel is the sentinel value used in a project filter slice to
// mean "issues that have no project assigned".
const noProjectSentinel = "__no_project__"

// QueryCandidateIssues fetches paginated issues by project + state filter.
const QueryCandidateIssues = `
query ItervoxLinearPoll($projectSlug: String!, $stateNames: [String!]!, $first: Int!, $relationFirst: Int!, $after: String) {
  issues(filter: {project: {slugId: {eq: $projectSlug}}, state: {name: {in: $stateNames}}, trashedAt: {null: true}}, first: $first, after: $after) {
    nodes {
      id
      identifier
      title
      description
      priority
      state { name }
      branchName
      url
      labels { nodes { name } }
      inverseRelations(first: $relationFirst) {
        nodes {
          type
          issue { id identifier url state { name } }
        }
      }
      createdAt
      updatedAt
    }
    pageInfo { hasNextPage endCursor }
  }
}`

// QueryIssueDetail fetches a single issue by ID with full details including comments.
const QueryIssueDetail = `
query ItervoxIssueDetail($id: String!) {
  issue(id: $id) {
    id
    identifier
    title
    description
    priority
    state { name }
    branchName
    url
    labels { nodes { name } }
    inverseRelations(first: 50) {
      nodes {
        type
        issue { id identifier url state { name } }
      }
    }
    comments(first: 50, orderBy: createdAt) {
      nodes {
        id
        body
        createdAt
        user { id name }
      }
    }
    createdAt
    updatedAt
  }
}`

// QueryCreateIssueContext fetches the source issue's team and project so a
// follow-up issue can be created in the same workspace context.
const QueryCreateIssueContext = `
query ItervoxResolveCreateIssueContext($id: String!) {
  issue(id: $id) {
    team {
      id
      states {
        nodes {
          id
          name
        }
      }
    }
    project {
      id
    }
  }
}`

// QueryCandidateIssuesAll fetches paginated issues by state only — no project filter.
// Used when the runtime project filter is set to "all issues".
const QueryCandidateIssuesAll = `
query ItervoxLinearPollAll($stateNames: [String!]!, $first: Int!, $relationFirst: Int!, $after: String) {
  issues(filter: {state: {name: {in: $stateNames}}, trashedAt: {null: true}}, first: $first, after: $after) {
    nodes {
      id
      identifier
      title
      description
      priority
      state { name }
      branchName
      url
      labels { nodes { name } }
      inverseRelations(first: $relationFirst) {
        nodes {
          type
          issue { id identifier url state { name } }
        }
      }
      createdAt
      updatedAt
    }
    pageInfo { hasNextPage endCursor }
  }
}`

// QueryCandidateIssuesNoProject fetches paginated issues that have no project
// assigned, filtered by state.
const QueryCandidateIssuesNoProject = `
query ItervoxLinearPollNoProject($stateNames: [String!]!, $first: Int!, $relationFirst: Int!, $after: String) {
  issues(filter: {project: {null: true}, state: {name: {in: $stateNames}}, trashedAt: {null: true}}, first: $first, after: $after) {
    nodes {
      id
      identifier
      title
      description
      priority
      state { name }
      branchName
      url
      labels { nodes { name } }
      inverseRelations(first: $relationFirst) {
        nodes {
          type
          issue { id identifier url state { name } }
        }
      }
      createdAt
      updatedAt
    }
    pageInfo { hasNextPage endCursor }
  }
}`

// QueryListProjects fetches up to 100 projects visible to the API key.
// Used for the interactive project picker.
const QueryListProjects = `
query ItervoxListProjects {
  projects(first: 100) {
    nodes {
      id
      name
      slugId
    }
  }
}`

// QueryIssuesByIDs fetches issues by ID list for reconciliation (uses [ID!] type).
const QueryIssuesByIDs = `
query ItervoxLinearIssuesById($ids: [ID!]!, $first: Int!, $relationFirst: Int!) {
  issues(filter: {id: {in: $ids}}, first: $first) {
    nodes {
      id
      identifier
      title
      description
      priority
      state { name }
      branchName
      url
      labels { nodes { name } }
      inverseRelations(first: $relationFirst) {
        nodes {
          type
          issue { id identifier url state { name } }
        }
      }
      createdAt
      updatedAt
    }
  }
}`
