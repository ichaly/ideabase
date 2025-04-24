SELECT jsonb_build_object('areaList', __sj_0.json) AS __root
FROM (
        SELECT true
    ) AS __root_x
    LEFT OUTER JOIN LATERAL (
        SELECT COALESCE(jsonb_agg(__sj_0.json), '[]') AS json
        FROM (
                SELECT to_jsonb(__sr_0.*) AS json
                FROM (
                        SELECT "sys_area_0"."id" AS "id", "sys_area_0"."name" AS "name"
                        FROM (
                                SELECT "sys_area"."id", "sys_area"."name"
                                FROM "sys_area"
                                LIMIT ?
                            ) AS "sys_area_0"
                    ) AS "__sr_0"
            ) AS "__sj_0"
    ) AS "__sj_0" ON true