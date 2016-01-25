package db

import (
	"database/sql"
	"github.com/Cepave/hbs/model"
	commonModel "github.com/Cepave/common/model"
	"log"
	"time"
)

// Inserts a new agent or updates existing one
func RefreshAgentInfo(agent *model.NqmAgent) (err error) {
	var result sql.Result

	/**
	 * Update the data
	 */
	if result, err = DB.Exec(
		`
		INSERT INTO nqm_agent(ag_connection_id, ag_hostname, ag_ip_address)
		VALUES(?, ?, ?)
		ON DUPLICATE KEY UPDATE
			ag_hostname = VALUES(ag_hostname),
			ag_ip_address = VALUES(ag_ip_address)
		`,
		agent.ConnectionId(),
		agent.Hostname(),
		[]byte(agent.IpAddress),
	)
		err != nil {

		log.Printf("Cannot refresh agent: [%v]. Error: %v", agent.ConnectionId(), err)
		return
	}
	// :~)

	/**
	 * Loads id from auto-generated PK
	 */
	lastId, _ := result.LastInsertId()
	agent.Id = int(lastId)
	// :~)

	return
}

// Gets the data of agent for RPC
//
// If there is no need to perform ping task, this method would return nil as result.
//
// Reasons for not doing ping task:
// 1) No ping task configuration
// 2) The period is overed yet
func GetAndRefreshNeedPingAgentForRpc(agentId int, checkedTime time.Time) (result *commonModel.NqmAgent, err error) {
	result = &commonModel.NqmAgent {
		Id: agentId,
	}

	err = inTx(func(tx *sql.Tx) (err error) {
		if err = tx.QueryRow(
			/**
			 * Gets one row if the executing of ping task is needed
			 *
			 * Gets no row if:
			 * 1) No ping task configuration
			 * 2) The period is overed yet
			 * 3) The agent is disabled
			 */
			`
			SELECT ag_isp_id, ag_pv_id, ag_ct_id
			FROM nqm_agent AS ag
				INNER JOIN
				(
					SELECT pt_ag_id
					FROM nqm_ping_task
					WHERE pt_ag_id = ?
						/* Check if the period is overed since last time executed */
						AND TIMESTAMPDIFF(
							MINUTE,
							IFNULL(pt_time_last_execute, FROM_UNIXTIME(0)), /* Use the very first time */
							FROM_UNIXTIME(?)
						) >= pt_period
						/* :~) */
				) AS pt
				ON ag.ag_id = pt.pt_ag_id
					AND ag.ag_status & b'00000001' = b'00000001'
			`,
			// :~)
			agentId, checkedTime.Unix(),
		).Scan(&result.IspId, &result.ProvinceId, &result.CityId)
			err != nil {

			/**
			 * There is no need to perform ping task
			 */
			result = nil
			if err == sql.ErrNoRows {
				err = nil
			}
			// :~)

			return
		}

		/**
		 * Updates the last execute
		 */
		if _, err = DB.Exec(
			`
			UPDATE nqm_ping_task
			SET pt_time_last_execute = FROM_UNIXTIME(?)
			WHERE pt_ag_id = ?
			`,
			checkedTime.Unix(), agentId,
		)
			err != nil {

			result = nil
		}
		// :~)

		return
	})

	return
}

const (
	NO_PING_TASK = 0
	HAS_PING_TASK_WITH_FILTER = 1
	HAS_PING_TASK_ALL_MATCHING = 2
)

// Gets the targets(to be probed) for RPC
func GetTargetsByAgentForRpc(agentId int) (targets []commonModel.NqmTarget, err error) {
	var taskState int

	if taskState, err = getPingTaskState(agentId)
		err != nil {
		return
	}

	var rows *sql.Rows

	switch taskState {
	case NO_PING_TASK:
		targets = make([]commonModel.NqmTarget, 0)
		return
	case HAS_PING_TASK_WITH_FILTER:
		rows, err = loadTargetsByFilter(agentId)
	case HAS_PING_TASK_ALL_MATCHING:
		rows, err = loadAllTargets()
	}

	if err != nil { return }

	/**
	 * Converts the data to NQM targets for RPC
	 */
	defer rows.Close()
	targets = make([]commonModel.NqmTarget, 0, 4)
	for rows.Next() {
		targets = append(targets, commonModel.NqmTarget{})
		var currentTarget *commonModel.NqmTarget = &targets[len(targets) - 1]
		currentTarget.NameTag = commonModel.UNDEFINED_STRING

		var nameTag sql.NullString

		rows.Scan(
			&currentTarget.Id,
			&currentTarget.Host,
			&currentTarget.IspId,
			&currentTarget.ProvinceId,
			&currentTarget.CityId,
			&nameTag,
		)

		if nameTag.Valid {
			currentTarget.NameTag = nameTag.String
		}
	}
	// :~)

	return
}

func loadAllTargets() (*sql.Rows, error) {
	return DB.Query(
		`
		SELECT tg_id, tg_host, tg_isp_id, tg_pv_id, tg_ct_id, tg_name_tag
		FROM nqm_target
		`,
	)
}
func loadTargetsByFilter(agentId int) (*sql.Rows, error) {
	return DB.Query(
		`
		/* Matched target by ISP */
		SELECT tg_id, tg_host, tg_isp_id, tg_pv_id, tg_ct_id, tg_name_tag
		FROM nqm_target tg
			INNER JOIN
			nqm_pt_target_filter_isp AS tfisp
			ON tfisp.tfisp_pt_ag_id = ?
				AND tg.tg_isp_id = tfisp.tfisp_isp_id
		/* :~) */
		UNION
		/* Matched target by province */
		SELECT tg_id, tg_host, tg_isp_id, tg_pv_id, tg_ct_id, tg_name_tag
		FROM nqm_target tg
			INNER JOIN
			nqm_pt_target_filter_province AS tfpv
			ON tfpv.tfpv_pt_ag_id = ?
				AND tg.tg_pv_id = tfpv.tfpv_pv_id
		/* :~) */
		UNION
		/* Matched target by city */
		SELECT tg_id, tg_host, tg_isp_id, tg_pv_id, tg_ct_id, tg_name_tag
		FROM nqm_target tg
			INNER JOIN
			nqm_pt_target_filter_city AS tfct
			ON tfct.tfct_pt_ag_id = ?
				AND tg.tg_ct_id = tfct.tfct_ct_id
		/* :~) */
		UNION
		/* Matched target by name tag */
		SELECT tg_id, tg_host, tg_isp_id, tg_pv_id, tg_ct_id, tg_name_tag
		FROM nqm_target tg
			INNER JOIN
			nqm_pt_target_filter_name_tag AS tfnt
			ON tfnt.tfnt_pt_ag_id = ?
				AND tg.tg_name_tag = tfnt.tfnt_name_tag
		/* :~) */
		UNION
		/* Matched target which to be probed by all */
		SELECT tg_id, tg_host, tg_isp_id, tg_pv_id, tg_ct_id, tg_name_tag
		FROM nqm_target tg
		WHERE tg_probed_by_all = true
		/* :~) */
		`,
		agentId, agentId, agentId, agentId,
	)
}

func getPingTaskState(agentId int) (result int, err error) {
	result = NO_PING_TASK

	if err = DB.QueryRow(
		`
		SELECT COUNT(pt_ag_id)
		FROM nqm_ping_task
		WHERE pt_ag_id = ?
		`,
		agentId,
	).Scan(&result)
		err != nil {
		return
	}

	/**
	 * Without ping task
	 */
	if result == NO_PING_TASK {
		return
	}
	// :~)

	/**
	 * Checks whether there is any filter configuration for the ping task
	 */
	var hasFilter int
	if err = DB.QueryRow(
		`
		SELECT COUNT(*)
		FROM (
			/* Get any condition of name tag filter  */
			SELECT tfnt.tfnt_pt_ag_id
			FROM nqm_pt_target_filter_name_tag AS tfnt
			WHERE tfnt.tfnt_pt_ag_id = ?
			LIMIT 1
			/* :~) */
			UNION
			/* Get any condition of ISP filter  */
			SELECT tfisp.tfisp_pt_ag_id
			FROM nqm_pt_target_filter_isp AS tfisp
			WHERE tfisp.tfisp_pt_ag_id = ?
			LIMIT 1
			/* :~) */
			UNION
			/* Get any condition of province filter  */
			SELECT tfpv.tfpv_pt_ag_id
			FROM nqm_pt_target_filter_province AS tfpv
			WHERE tfpv.tfpv_pt_ag_id = ?
			LIMIT 1
			/* :~) */
			UNION
			/* Get any condition of city filter  */
			SELECT tfct.tfct_pt_ag_id
			FROM nqm_pt_target_filter_city AS tfct
			WHERE tfct.tfct_pt_ag_id = ?
			LIMIT 1
			/* :~) */
		) AS filter
		`,
		agentId, agentId, agentId, agentId,
	).Scan(&hasFilter)
		err != nil {
		return
	}
	// :~)

	if hasFilter == 1 {
		result = HAS_PING_TASK_WITH_FILTER
	} else {
		result = HAS_PING_TASK_ALL_MATCHING
	}

	return
}
